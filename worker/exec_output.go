package worker

import (
	"io"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/combined_log"
	"github.com/turtlemonvh/blanket/lib/follow"
	"github.com/turtlemonvh/blanket/lib/timing"
)

// ExecOutput is where one task run's output goes: the two per-stream log
// files the child writes into, and (when enabled) the combined record of
// how the two interleaved.
//
// The child is given the two files *directly* — cmd.Stdout is the
// *os.File, as it has always been — and the worker builds the combined
// record by tailing those files rather than by standing in the middle of
// them. That is deliberate, and it is the second design for this
// (turtlemonvh/blanket#104 review): copying the streams through the
// worker meant the child got a pipe, and a pipe has an owner that goes
// away. A task that backgrounds a process and exits leaves that process
// holding the write end; os/exec then either waits for it forever or, on
// a WaitDelay, closes the pipe under it — at which point the orphan's
// output is gone and the orphan may itself die of SIGPIPE. Worker
// restarts are about to become routine (turtlemonvh/blanket#23), so
// "output disappears a couple of seconds after the task exits" would
// have been a common failure, and losing logs is worse than losing
// ordering. Writing into a file has no such owner: an orphan keeps
// appending to blanket.stdout.log for as long as it lives, exactly as it
// did before any of this existed.
//
// This type also holds the two file handles for the monitoring
// goroutine, which Sync()s them on its poll interval and can no longer
// find them by type-asserting cmd.Stdout now that the worker owns them
// deliberately.
type ExecOutput struct {
	stdout     *os.File
	stderr     *os.File
	stdoutPath string
	stderrPath string
	combined   *combined_log.Writer
	tails      []*streamTail
	finishOnce sync.Once
}

const (
	// combinedTailGrace is how long the worker keeps tailing a finished
	// task's two log files, after both have stopped growing, before it
	// closes the combined record. Unscaled; timeMultiplier is applied at
	// use.
	//
	// It exists for the same reason server/serve_stream.go's
	// logDrainGrace does, and is deliberately the same length: the last
	// lines of a task routinely land on disk a moment after the process
	// itself is gone, and a tailer sees them a moment after that.
	combinedTailGrace = 400 * time.Millisecond
	// combinedTailMax bounds that wait, so a task that leaves something
	// behind chattering into its log files can't hold up the finish
	// report indefinitely. Mirrors logDrainMax.
	combinedTailMax = 3 * time.Second
	// combinedTailPoll is how often the drain re-checks the two files'
	// sizes while waiting for them to go quiet.
	combinedTailPoll = 50 * time.Millisecond
)

// streamTail follows one of the two per-stream log files and records
// every line it sees into the combined log, tagged with its stream.
type streamTail struct {
	name   string
	path   string
	sink   io.Writer
	tailer *follow.Follower
	done   chan struct{}
	// consumed is how many bytes of complete lines have been recorded.
	// Written only by the forwarding goroutine and read only after that
	// goroutine has exited (<-done), so it needs no lock — and the
	// residual read at stop starts from it, which is what makes the
	// combined record cover every byte the files held when tailing
	// stopped.
	consumed int64
}

// StartTailing begins recording the combined log. Called once, right
// after the child process starts, so the tailers are watching from the
// first byte the task writes.
//
// Failing to open a tailer is a warning, not an error: the combined
// record is a supplementary view of ordering, and a task must not fail
// because blanket couldn't build one.
func (o *ExecOutput) StartTailing(taskId string) {
	if o == nil || o.combined == nil {
		return
	}
	for _, s := range []struct{ name, p string }{
		{combined_log.StreamStdout, o.stdoutPath},
		{combined_log.StreamStderr, o.stderrPath},
	} {
		t, err := follow.Open(s.p, follow.Options{
			// From the first byte: the file was created empty moments
			// ago, and anything the child managed to write before this
			// call must still be recorded.
			Offset: 0,
			Whence: io.SeekStart,
			Poll:   tailUsesPolling,
		})
		if err != nil {
			log.WithFields(log.Fields{
				"err":    err.Error(),
				"taskId": taskId,
				"stream": s.name,
			}).Warn("failed to tail a task log file; the combined log will be missing that stream")
			continue
		}
		st := &streamTail{
			name:   s.name,
			path:   s.p,
			sink:   o.combined.Stream(s.name),
			tailer: t,
			done:   make(chan struct{}),
		}
		o.tails = append(o.tails, st)
		go st.forward()
	}
}

// forward records each line the tailer delivers. The order the two
// forwarding goroutines reach the combined writer's mutex is the order
// the file records — "as the tailer saw them", which is the same basis
// the live log view shows lines on.
func (st *streamTail) forward() {
	defer close(st.done)
	for line := range st.tailer.Lines() {
		if line.Err != nil {
			// Not file content, so it is neither recorded nor counted
			// against the offset.
			continue
		}
		// Put back the newline the follower trimmed: the combined writer
		// splits on newlines, and its Write never reports an error.
		st.sink.Write([]byte(line.Text + "\n"))
		// From the line's own start offset rather than by accumulating
		// lengths, so the residual read below picks up at exactly the
		// byte after the last line recorded even if the follower had to
		// reopen the file underneath us.
		st.consumed = line.Offset + int64(len(line.Text)) + 1
	}
}

// Sync flushes everything this run writes, so a reader tailing any of
// the three files sees the same moment in the task's output.
func (o *ExecOutput) Sync() {
	if o == nil {
		return
	}
	if o.stdout != nil {
		o.stdout.Sync()
	}
	if o.stderr != nil {
		o.stderr.Sync()
	}
	o.combined.Sync()
}

// Finish closes the combined record: it keeps tailing until the two log
// files have been quiet for combinedTailGrace (bounded by
// combinedTailMax), then stops the tailers, records whatever they had
// not reached, and closes the file.
//
// ProcessOne calls this after cmd.Wait() and *before* it writes the
// "exited" journal entry or reports the task finished, so a task that
// the UI shows as FINISHED always has a complete combined log rather
// than one still being appended to.
//
// The limitation this leaves is deliberate and documented (see
// docs/task_flow.md): a process the task orphaned and that is still
// writing when the grace window closes keeps landing in
// blanket.stdout.log / blanket.stderr.log — which the per-stream views
// and the raw /results routes always show in full — but is not in the
// combined record, and so not in the `both` view. Losing ordering for an
// orphan's late output is a much smaller price than losing the output.
func (o *ExecOutput) Finish() {
	if o == nil {
		return
	}
	o.finishOnce.Do(o.finish)
}

func (o *ExecOutput) finish() {
	if len(o.tails) > 0 {
		o.waitForQuiet()
		for _, st := range o.tails {
			st.stop()
		}
		for _, st := range o.tails {
			st.drainResidual()
		}
		o.tails = nil
	}
	if err := o.combined.Close(); err != nil {
		log.WithField("err", err.Error()).Warn("failed to write the task's combined log; its interleaved history may be incomplete")
	}
}

// waitForQuiet blocks until neither log file has grown for a grace
// window, or until the hard bound expires. The files' sizes are the
// signal rather than the tailers' progress: a tailer can be a poll
// interval behind, and what this needs to know is whether anything is
// still *writing*.
func (o *ExecOutput) waitForQuiet() {
	grace := timing.Scale(combinedTailGrace)
	tick := timing.Scale(combinedTailPoll)
	hardDeadline := time.Now().Add(timing.Scale(combinedTailMax))

	// Sync before the first look: the last lines a task wrote are only
	// on disk for certain once its file handles have been flushed.
	o.Sync()
	last := o.streamSizes()
	quietSince := time.Now()
	for {
		time.Sleep(tick)
		o.Sync()
		if sizes := o.streamSizes(); sizes != last {
			last = sizes
			quietSince = time.Now()
		}
		now := time.Now()
		if now.Sub(quietSince) >= grace || now.After(hardDeadline) {
			return
		}
	}
}

// streamSizes is the pair of file sizes, comparable so a change in
// either is one equality test. An unreadable file reports -1, which is
// stable and so reads as "quiet" rather than as endless activity.
func (o *ExecOutput) streamSizes() [2]int64 {
	return [2]int64{fileSize(o.stdoutPath), fileSize(o.stderrPath)}
}

func fileSize(p string) int64 {
	if p == "" {
		return -1
	}
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}

// stop ends the tailer and waits for the forwarding goroutine, so
// consumed is final and nothing can still be appending to the combined
// writer when the residual read runs.
func (st *streamTail) stop() {
	// Stop releases the follower's watch too -- the job hpcloud/tail
	// split out into a separate Cleanup() call.
	st.tailer.Stop()
	<-st.done
}

// drainResidual records whatever the tailer had not reached when it
// stopped, reading the file directly from the offset the tailer got to.
//
// Two things land here. The bytes appended in the moments between the
// tailer's last read and its stop — without this they would be in the
// per-stream file but not the combined record, which is the sort of
// silent gap this whole change exists to avoid. And the task's final
// line when it ends without a newline: a follower never delivers one
// while following (it seeks back and waits for a newline that, for a
// finished task, is not coming -- see lib/follow), so the file's last fragment would
// otherwise be dropped. The combined writer buffers it and Close()
// flushes it as a whole record.
func (st *streamTail) drainResidual() {
	f, err := os.Open(st.path)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"stream": st.name,
			"path":   st.path,
		}).Warn("could not reread a task log file; the end of its combined log may be short")
		return
	}
	defer f.Close()
	if _, err := f.Seek(st.consumed, io.SeekStart); err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"stream": st.name,
			"offset": st.consumed,
		}).Warn("could not seek a task log file; the end of its combined log may be short")
		return
	}
	if _, err := io.Copy(st.sink, f); err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"stream": st.name,
		}).Warn("could not read the tail of a task log file into its combined log")
	}
}

// Close finishes the combined record and releases the two log files.
// Safe on a partially built ExecOutput — SetupExecutionDirectory returns
// one even when it fails partway, so the caller can defer this
// unconditionally — and safe after Finish has already run.
func (o *ExecOutput) Close() {
	if o == nil {
		return
	}
	o.Finish()
	if o.stdout != nil {
		o.stdout.Close()
	}
	if o.stderr != nil {
		o.stderr.Close()
	}
}

// combinedLogEnabled reports whether this worker records the interleaved
// combined log alongside the two per-stream files (`workers.combinedLog`,
// default true).
//
// An unset key means enabled: the default is registered in
// command.InitializeConfig, and anything that runs a worker without
// going through it (a test, an embedder) should get the current
// behaviour rather than the legacy one.
//
// Turning it off costs nothing but the file: the child's stdout and
// stderr are the two log files either way, so a task runs, finishes and
// logs identically. It is there for an operator who does not want a
// third file per task, or the extra tailer per running task, and for
// bisecting anything that looks like it might be the recorder's fault.
func combinedLogEnabled() bool {
	if !viper.IsSet("workers.combinedLog") {
		return true
	}
	return viper.GetBool("workers.combinedLog")
}
