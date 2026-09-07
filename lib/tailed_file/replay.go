package tailed_file

// ReplayTail: read what a log file already holds, then follow it from the
// byte offset the read stopped at.
//
// Why this exists alongside Follow/TailedFile (turtlemonvh/blanket#104
// review): a TailedFile is *shared* between every subscriber of a path,
// and how much history a new subscriber gets is an accident of who else
// is already watching.
//
//   - Nobody watching: StartTailedFile opens a tailer at the start of the
//     file (or 5000 bytes back from the end, DefaultFileOffset, if it is
//     bigger than that) and the whole file arrives down the live path, so
//     it *looks* like a replay.
//   - Somebody already watching: the tailer is already past the history,
//     so the new subscriber gets only TailedFile.PastLines -- a shared
//     ring of the last DefaultLinesKept (100) lines that tailer has seen.
//
// Toggling a task's log pane between stdout, stderr and both is exactly
// the sequence that leaves files warm, so the combined view kept coming
// back with a ring-buffer's worth of history or none at all. There is no
// per-subscriber start position to fix that with: the tailer is shared.
//
// So a caller that needs a *defined* amount of history takes its own,
// unshared tail instead. ReplayAndFollow reads the complete lines on disk
// itself, remembers exactly how many bytes it consumed, and starts its
// tailer at that offset -- nothing is replayed twice and nothing falls
// through the seam. History comes back as a slice rather than down the
// channel so a caller following two files can order the two histories
// deliberately (see the UI's combined log stream) instead of having them
// race.
//
// The cost is one tailer (one goroutine, one poller) per caller rather
// than per path. That is the right trade for the UI's log pane, which has
// a handful of viewers at a time; a fan-out-heavy consumer should keep
// using Follow.

import (
	"bufio"
	"expvar"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/hpcloud/tail"
)

// liveLineBuffer is how many live lines ReplayAndFollow will hold for a
// consumer that is momentarily busy (an SSE handler flushing to a slow
// client) before the tailer has to wait for it.
const liveLineBuffer = 256

var nReplayTails = expvar.NewInt("nReplayTails")

// ReplayTail is one file's history plus a live feed of everything written
// after it. Stop must be called exactly once, by the caller, on every exit
// path.
type ReplayTail struct {
	// History is the complete lines already on disk when the tail was
	// opened, oldest first, newline stripped -- the same shape the live
	// channel carries.
	History []string
	// Truncated reports that the file held more lines than maxHistory and
	// History is only its tail.
	Truncated bool
	// Offset is the byte position History ended at, which is where the
	// live tailer was started. A partial last line -- one the writer had
	// not finished when the file was read -- is left for the tailer, so it
	// arrives whole rather than twice.
	Offset int64

	// Lines carries every line appended after Offset. Closed when the
	// underlying tailer stops.
	Lines <-chan string

	tailer   *tail.Tail
	done     chan struct{}
	finished chan struct{}
	stopOnce sync.Once
}

// ReplayAndFollow opens p, returns the last maxHistory complete lines it
// holds (all of them when maxHistory <= 0), and follows the file from
// where that read stopped.
//
// A missing file is an error, as it is for Follow: the caller retries once
// the worker has created it.
func ReplayAndFollow(p string, maxHistory int) (*ReplayTail, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	history, offset, truncated, err := readCompleteLines(f, maxHistory)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}

	// Poll rather than inotify, for the same cross-platform reason
	// StartTailedFile polls. DiscardingLogger because a tailer per open
	// log pane would otherwise narrate every reopen to the server's
	// stderr, outside logrus.
	tailer, err := tail.TailFile(p, tail.Config{
		Location: &tail.SeekInfo{Offset: offset, Whence: io.SeekStart},
		Follow:   true,
		Poll:     true,
		Logger:   tail.DiscardingLogger,
	})
	if err != nil {
		return nil, err
	}

	lines := make(chan string, liveLineBuffer)
	rt := &ReplayTail{
		History:   history,
		Truncated: truncated,
		Offset:    offset,
		Lines:     lines,
		tailer:    tailer,
		done:      make(chan struct{}),
		finished:  make(chan struct{}),
	}
	nReplayTails.Add(1)

	// Forwarding goroutine. The select on done is what keeps Stop from
	// wedging: tail's Lines channel is unbuffered and its reader goroutine
	// blocks on the send, so a consumer that walks away mid-line would
	// otherwise leave tail.Stop()'s Wait() blocked forever. On done we
	// keep draining until tail closes the channel, which is the only way
	// its reader can finish.
	go func() {
		defer close(rt.finished)
		defer close(lines)
		for line := range tailer.Lines {
			select {
			case lines <- line.Text:
			case <-rt.done:
				for range tailer.Lines {
				}
				return
			}
		}
	}()

	return rt, nil
}

// Stop releases the tailer and closes Lines. Safe to call more than once,
// and safe to call from a consumer that has stopped reading Lines.
func (rt *ReplayTail) Stop() {
	if rt == nil {
		return
	}
	rt.stopOnce.Do(func() {
		close(rt.done)
		rt.tailer.Stop()
		rt.tailer.Cleanup()
		<-rt.finished
		nReplayTails.Add(-1)
	})
}

// readCompleteLines reads every newline-terminated line from f, keeping at
// most the last maxHistory of them, and reports how many bytes of complete
// lines it consumed.
//
// Only complete lines count. A trailing fragment with no newline is a line
// the task is still writing: it is neither returned nor counted, so the
// tailer picks it up when the writer finishes it.
//
// Lines are trimmed the way hpcloud/tail trims them (TrimRight of "\n" and
// nothing else) so a line's text is identical whether it arrived through
// the history or through the live channel.
func readCompleteLines(f *os.File, maxHistory int) (lines []string, offset int64, truncated bool, err error) {
	r := bufio.NewReader(f)
	for {
		s, rerr := r.ReadString('\n')
		if rerr != nil {
			if rerr == io.EOF {
				// s is the unterminated remainder, if any. Leave it.
				break
			}
			return nil, 0, false, rerr
		}
		offset += int64(len(s))
		lines = append(lines, strings.TrimRight(s, "\n"))

		// Trim in blocks rather than one line at a time: re-slicing off
		// the front on every line would let the backing array grow with
		// the file instead of with the cap.
		if maxHistory > 0 && len(lines) > 2*maxHistory {
			lines = append(lines[:0], lines[len(lines)-maxHistory:]...)
			truncated = true
		}
	}
	if maxHistory > 0 && len(lines) > maxHistory {
		lines = lines[len(lines)-maxHistory:]
		truncated = true
	}
	return lines, offset, truncated, nil
}
