// Package combined_log records how a task's two output streams
// interleaved (turtlemonvh/blanket#104 review).
//
// The problem it exists to solve: the worker used to hand the child
// process the two log files directly (cmd.Stdout = blanket.stdout.log,
// cmd.Stderr = blanket.stderr.log), so the kernel wrote each stream into
// its own file and *nothing on disk recorded the order the two were
// produced in*. A reader coming along later -- the UI's combined log
// pane, the completion payload, a person with `cat` -- can merge the two
// files, but only by guessing: file order says nothing about how a
// stdout line and a stderr line were spaced relative to each other. The
// UI's `both` view therefore had to replay history as all-of-stdout then
// all-of-stderr, while lines arriving live interleaved by arrival, so
// the same output looked different depending on whether you were
// watching when it happened.
//
// So the worker *tails* both files as the task writes them and appends
// one NDJSON record per completed line, in the order it saw them land,
// to a third file in the result dir. That file is the ordering the two
// per-stream files can't carry. They themselves are untouched: the child
// still gets them as its own fd 1 and 2, written byte-for-byte as
// before, and every existing reader of them is unaffected.
//
// Tailing rather than standing in the middle is the load-bearing choice
// (turtlemonvh/blanket#104 review). Copying the streams through the
// worker -- cmd.Stdout = io.MultiWriter(logFile, recorder) -- was tried
// first, and it makes the child's stdout a pipe the worker owns. A pipe
// has an owner that goes away: a task that starts a process and exits
// leaves that process holding the write end, so cmd.Wait() blocks for
// its whole life, and bounding that with cmd.WaitDelay closes the pipe
// under it -- its output lost, and the process itself liable to die of
// SIGPIPE. A file has no owner, so an orphan goes on appending to
// blanket.stdout.log for as long as it lives. Missing logs are worse
// than missing ordering.
//
// The cost of tailing is one bounded gap, and it is documented where a
// user meets it (docs/task_flow.md): the worker stops tailing a short
// grace window after the task exits, so output from a process that
// outlives the task is in the per-stream files and every route that
// reads them, but not in this record.
//
// "The order the worker saw them land" is the honest description of what
// the file records, and the limit of what it can be: it is exactly the
// basis the live log view already shows lines on, not a kernel-level
// total order. Two lines written microseconds apart on different streams
// -- or a task that writes everything at once and exits, leaving two
// tailers to be scheduled in whatever order the runtime picks -- can be
// recorded either way round. What the file guarantees is that a task's
// replayed history and its live output are ordered the same way, which
// is the thing that was actually wrong.
//
// The record's fields are deliberately the field names of
// server.LogEvent (`ts`, `stream`, `seq`, `line`), so the on-disk record
// and the structured `log` event a client reads off the wire cannot
// drift into two different shapes. The one difference is documented on
// Record.Ts: milliseconds here, because sub-second ordering is the whole
// point of the file.
//
// The package lives under lib/ rather than in `worker` or `server`
// because both write and read it, and neither may import the other.
package combined_log

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// FileName is what the worker calls this file inside a task's result
// dir. Defined here rather than in each package that joins it to a
// result dir, so there is one spelling of it.
const FileName = "blanket.combined.ndjson"

// The stream names a task's two outputs are recorded under. Same
// strings as server.LogStreamStdout / LogStreamStderr -- named here too
// because the worker writes them and cannot import the server package.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// maxLineBytes bounds how much of an unterminated line is buffered
// before it is written out as a record anyway.
//
// A line is only recorded once its newline arrives, which is what keeps
// a record whole. A task that writes megabytes without ever emitting a
// newline (a progress bar redrawing with \r, say) would otherwise grow
// that buffer without limit inside the worker. Cutting at a fixed size
// costs a line split in an unusual case; not cutting costs the worker's
// memory in the same case.
const maxLineBytes = 1 << 20

// Record is one line of task output, as stored.
//
// Field names and meanings match server.LogEvent, minus the envelope's
// taskId/type (the file is per-task, and every record in it is a log
// line, so both would be a constant repeated on every row).
type Record struct {
	// Ts is when the worker saw the line's newline, in unix
	// milliseconds. Milliseconds rather than the envelope's seconds
	// because this file exists to record ordering, and a whole second
	// is thousands of lines of output.
	Ts int64 `json:"ts"`
	// Stream is which of the child's outputs the line came from:
	// "stdout" or "stderr" (server.LogStreamStdout / LogStreamStderr).
	Stream string `json:"stream"`
	// Seq counts lines within one stream of one run, from 1 -- the same
	// meaning server.LogEvent.Seq has, so a reader can hand the record
	// straight over as an event. It is not a position in the file: the
	// file's own order is the interleaving.
	Seq int `json:"seq"`
	// Line is the line's text, with its trailing newline removed and
	// nothing else trimmed -- the same trimming tailed_file applies, so
	// a line reads identically whichever surface delivered it.
	Line string `json:"line"`
}

// ParseRecord decodes one line of the file.
//
// ok is false for anything that isn't a usable record: a blank line, a
// line that isn't JSON, or one with no stream to attribute it to. A
// caller rendering the file should skip those rather than fail -- the
// file is append-only and written a line at a time, and refusing to show
// a task's output because one row was mangled by a full disk would be
// the wrong trade.
func ParseRecord(s string) (Record, bool) {
	var r Record
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return Record{}, false
	}
	if r.Stream == "" {
		return Record{}, false
	}
	return r, true
}

// Writer appends records to one task's combined log.
//
// Streams are registered by name through Stream(); every write to any of
// them goes through the same mutex, and that serialization is what makes
// the file's order the arrival order. Partial lines are buffered per
// stream until their newline arrives, so a record is always a whole
// line, and interleaving is recorded at line granularity rather than at
// the granularity of however the child's writes happened to be chunked.
type Writer struct {
	mu       sync.Mutex
	f        *os.File
	streams  map[string]*streamState
	closed   bool
	writeErr error
}

type streamState struct {
	buf []byte
	seq int
}

// Create makes (or truncates) the file at p and returns a Writer on it.
func Create(p string) (*Writer, error) {
	f, err := os.Create(p)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, streams: map[string]*streamState{}}, nil
}

// Stream returns an io.Writer that records everything written to it as
// lines of the named stream. The worker feeds one per stream from a
// tailer of that stream's log file.
//
// The returned writer never reports an error, and never a short write.
// A failure to record the interleaving must not be able to interrupt
// whatever is feeding it -- this file is a supplementary record of
// ordering, and a task must never look like it failed, or lose output
// from its other stream, because of a problem writing it. The error is
// kept for Close to report and log instead.
func (w *Writer) Stream(name string) io.Writer {
	if w == nil {
		return io.Discard
	}
	return streamWriter{w: w, name: name}
}

type streamWriter struct {
	w    *Writer
	name string
}

func (s streamWriter) Write(p []byte) (int, error) {
	s.w.append(s.name, p)
	return len(p), nil
}

func (w *Writer) append(name string, p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}

	st, ok := w.streams[name]
	if !ok {
		st = &streamState{}
		w.streams[name] = st
	}
	st.buf = append(st.buf, p...)

	for {
		i := bytes.IndexByte(st.buf, '\n')
		if i < 0 {
			break
		}
		w.emitLocked(name, st, string(st.buf[:i]))
		// copy-down rather than re-slice: re-slicing would keep the
		// whole (possibly huge) backing array alive for the life of the
		// run.
		st.buf = append(st.buf[:0], st.buf[i+1:]...)
	}

	if len(st.buf) >= maxLineBytes {
		w.emitLocked(name, st, string(st.buf))
		st.buf = st.buf[:0]
	}
}

// emitLocked writes one record. Caller holds the mutex.
func (w *Writer) emitLocked(name string, st *streamState, line string) {
	st.seq++
	b, err := json.Marshal(Record{
		Ts:     time.Now().UnixMilli(),
		Stream: name,
		Seq:    st.seq,
		Line:   line,
	})
	if err != nil {
		w.noteErr(err)
		return
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		w.noteErr(err)
	}
}

// noteErr keeps the first write error. Caller holds the mutex.
func (w *Writer) noteErr(err error) {
	if w.writeErr == nil {
		w.writeErr = err
	}
}

// Sync flushes the file to disk. The worker calls it on the same poll
// interval it Sync()s the two per-stream files, so a reader tailing any
// of the three sees them move together.
func (w *Writer) Sync() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	return w.f.Sync()
}

// Err reports the first write error the recorder swallowed, or nil.
func (w *Writer) Err() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeErr
}

// Close flushes whatever each stream had buffered without a final
// newline and closes the file. Safe to call more than once.
//
// The leftovers are written in a stable order (stdout before stderr,
// then anything else alphabetically) rather than map order, so a task
// that ends mid-line produces the same file every time.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.writeErr
	}

	for _, name := range w.flushOrderLocked() {
		st := w.streams[name]
		if len(st.buf) == 0 {
			continue
		}
		w.emitLocked(name, st, string(st.buf))
		st.buf = st.buf[:0]
	}

	w.closed = true
	if err := w.f.Close(); err != nil {
		w.noteErr(err)
	}
	return w.writeErr
}

func (w *Writer) flushOrderLocked() []string {
	names := make([]string, 0, len(w.streams))
	for name := range w.streams {
		names = append(names, name)
	}
	rank := map[string]int{StreamStdout: 0, StreamStderr: 1}
	sort.Slice(names, func(i, j int) bool {
		ri, oki := rank[names[i]]
		rj, okj := rank[names[j]]
		if oki != okj {
			return oki
		}
		if oki && okj && ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	return names
}
