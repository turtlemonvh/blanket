// Package follow yields lines from a file as it grows: blanket's own,
// small replacement for github.com/hpcloud/tail (turtlemonvh/blanket#142,
// following the dependency audit in #131).
//
// # Attribution
//
// The design is lifted, deliberately and with thanks, from
// github.com/hpcloud/tail (MIT, HPE Software Inc. / ActiveState Software
// Inc.) and its maintained fork github.com/nxadm/tail. What was borrowed
// is the *shape*, not the code — nothing here is a copy of theirs:
//
//   - The SeekInfo/offset model: a caller says where in the file to start
//     (Options.Offset + Options.Whence, the arguments of os.Seek), and the
//     follower delivers from exactly there. See hpcloud/tail's SeekInfo
//     and its `tail.Location` handling in tailFileSync.
//   - The line-reader semantics: bufio.ReadString('\n'), with the
//     delivered text being strings.TrimRight(s, "\n") — only the newline,
//     never a trailing "\r" (hpcloud/tail's readLine does exactly this,
//     and blanket's stored log text has always matched it).
//   - Holding an unterminated final line: on EOF with a partial line in
//     hand, seek back to where that line started and wait for its
//     newline, so a line is delivered once, whole, and never twice
//     (hpcloud/tail's `seekTo(offset)` on the EOF-with-partial branch).
//     lib/tailed_file's ReplayAndFollow and worker.ExecOutput's residual
//     read both depend on this.
//   - Reopen-on-truncate: a file shorter than the position we had reached
//     has been truncated, so reopen it and start from the beginning
//     (hpcloud/tail's waitForChanges -> changes.Truncated branch, which
//     always reopens); and the ReOpen (`tail -F`) behaviour of waiting for
//     a deleted/renamed file to come back.
//
// What is deliberately *not* here, because blanket never used it: rate
// limiting, MaxLineSize splitting, named-pipe support, MustExist, a
// pluggable logger, and gopkg.in/tomb.v1 for lifecycle (a done channel and
// a sync.Once are enough for one goroutine). Dropping hpcloud/tail also
// dropped its unmaintained gopkg.in/fsnotify.v1 and the vendored
// gopkg.in/tomb.v1 copy that only existed to serve it.
//
// # Wakeups
//
// A Follower learns that its file changed either from
// github.com/fsnotify/fsnotify (the maintained watcher, already in
// blanket's tree via viper) or from a polling ticker. Poll:true asks for
// the ticker outright; otherwise fsnotify is tried and the ticker is the
// fallback when it cannot be initialised. Either way a stat of the path is
// what actually classifies the change — an event is only ever a hint that
// it is worth looking. fsnotify watches the file's *parent directory*, not
// the file: a watch on an inode does not survive the file being replaced,
// and create/rename are exactly the events a follower needs to see.
//
// Even in fsnotify mode a slow ticker keeps running (notifyFallbackPoll),
// so a dropped or coalesced kernel event costs a second of latency rather
// than stalling the stream forever. Nothing here spins: every wait is a
// blocking select.
package follow

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	// DefaultPollInterval is how often a polling Follower stats its file.
	// 250ms is what hpcloud/tail polled at (its watch.POLL_DURATION), kept
	// so the latency of blanket's log views doesn't change with this
	// package.
	DefaultPollInterval = 250 * time.Millisecond

	// notifyFallbackPoll is how often an fsnotify-backed Follower stats its
	// file anyway. It is a safety net, not the mechanism: kernel event
	// queues can overflow, and a follower that missed the one event it was
	// waiting for would otherwise never wake again.
	notifyFallbackPoll = 1 * time.Second
)

// ErrFileRemoved is the terminal error of a Follower whose file was
// deleted, renamed, or replaced while Options.ReOpen was false. With
// ReOpen set, the Follower waits for the path to come back instead.
var ErrFileRemoved = errors.New("follow: followed file was removed or replaced")

// Line is one complete line of the file.
type Line struct {
	// Text is the line without its trailing newline. Only "\n" is
	// trimmed — a "\r" before it is part of the text, matching
	// hpcloud/tail and therefore matching every log line blanket has
	// stored to date.
	Text string
	// Offset is the byte offset the line starts at. Offset +
	// len(Text) + 1 is where the next line starts, which is what
	// worker.ExecOutput uses to know how much of a log file its
	// combined record already covers.
	Offset int64
	// Err is reserved for a non-fatal, per-line error. The current
	// implementation never sets it — every error a Follower can hit is
	// terminal and reported by Err() after Lines() closes — but callers
	// that range over Lines() are expected to skip a Line with a
	// non-nil Err rather than treat it as file content.
	Err error
}

// Options configure a Follower. The zero value follows a file from its
// first byte using fsnotify, and finishes with ErrFileRemoved if the file
// goes away.
type Options struct {
	// Offset and Whence say where to start, as the arguments of
	// os.Seek: Whence is io.SeekStart (the zero value), io.SeekCurrent
	// — equivalent to io.SeekStart on a freshly opened file — or
	// io.SeekEnd. The resolved position is clamped into [0, size]:
	// a negative resolved offset would be a seek error, and one past
	// the end would immediately look like a truncation and replay the
	// whole file.
	Offset int64
	Whence int

	// Poll asks for the polling ticker instead of fsnotify. blanket
	// polls wherever the file may not be local or the process may be
	// watching many of them (lib/tailed_file), and on Windows (see
	// worker/tail_watch_windows.go).
	Poll bool

	// PollInterval overrides DefaultPollInterval. Ignored when it is
	// <= 0.
	PollInterval time.Duration

	// ReOpen follows the path rather than the file, `tail -F`-style: a
	// deleted, renamed or replaced file is waited for and then followed
	// from its first byte. Without it, that is the end of the stream
	// (ErrFileRemoved).
	ReOpen bool
}

// Follower delivers the lines of one file as they are written. Open
// starts it; Stop ends it. Exactly one goroutine is running between
// those two calls.
type Follower struct {
	path string
	opts Options

	lines    chan Line
	done     chan struct{}
	finished chan struct{}
	stopOnce sync.Once

	// Owned by the run goroutine; nothing else touches them.
	file    *os.File
	reader  *bufio.Reader
	pos     int64
	scanned int64
	watcher *fsnotify.Watcher
	events  chan fsnotify.Event
	errs    chan error
	ticker  *time.Ticker

	// rounds counts trips round the run loop: one per "read what is
	// there, then wait". It exists so the no-spin property can be
	// asserted rather than assumed -- a follower parked on an
	// unterminated last line has to sit still, and the first cut of this
	// package instead ran that loop flat out, in the one place that did
	// not check for a stop. See the regression test.
	rounds atomic.Int64

	mu  sync.Mutex
	err error
}

// Open starts following path. A missing file is an error: every caller in
// blanket has already established that the file exists (the worker just
// created it, or the handler just read it), and reporting it here lets
// them retry rather than watch an empty stream.
func Open(path string, opts Options) (*Follower, error) {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	f := &Follower{
		path:     path,
		opts:     opts,
		lines:    make(chan Line),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		file:     file,
	}

	if err := f.seekStart(); err != nil {
		file.Close()
		return nil, err
	}
	f.reader = bufio.NewReader(file)

	// fsnotify on the parent directory, unless the caller asked to poll.
	// A failure to set it up is not an error: the ticker below covers the
	// same ground, just with more latency.
	tick := notifyFallbackPoll
	if opts.Poll {
		tick = opts.PollInterval
	} else if w, werr := fsnotify.NewWatcher(); werr == nil {
		if aerr := w.Add(filepath.Dir(path)); aerr != nil {
			w.Close()
			tick = opts.PollInterval
		} else {
			f.watcher = w
			f.events = w.Events
			f.errs = w.Errors
		}
	} else {
		tick = opts.PollInterval
	}
	f.ticker = time.NewTicker(tick)

	go f.run()
	return f, nil
}

// Lines is the channel every complete line arrives on. It is closed when
// the Follower finishes, whether from Stop or from an error; check Err
// afterwards to tell the two apart.
//
// The channel is unbuffered, as hpcloud/tail's was: a consumer that stops
// reading holds the follower at its last line rather than letting it race
// ahead into memory. Stop always breaks that send.
func (f *Follower) Lines() <-chan Line { return f.lines }

// Stop ends the Follower and returns once its goroutine has exited, so
// Lines is closed by the time it returns. Idempotent: a handler with both
// a defer and an explicit stop is normal. The returned error is Err's —
// nil for a Follower that was simply stopped.
func (f *Follower) Stop() error {
	f.stopOnce.Do(func() { close(f.done) })
	<-f.finished
	return f.Err()
}

// Err is the error the Follower finished with, or nil. Only meaningful
// once Lines has been closed (or Stop has returned).
func (f *Follower) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *Follower) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err == nil {
		f.err = err
	}
}

// seekStart resolves Options.Offset/Whence against the file's current size
// and leaves the handle there. See the Options doc for why the result is
// clamped rather than passed to os.Seek as-is.
func (f *Follower) seekStart() error {
	fi, err := f.file.Stat()
	if err != nil {
		return err
	}

	var target int64
	switch f.opts.Whence {
	case io.SeekStart, io.SeekCurrent:
		target = f.opts.Offset
	case io.SeekEnd:
		target = fi.Size() + f.opts.Offset
	default:
		return errors.New("follow: invalid Whence")
	}
	if target < 0 {
		target = 0
	}
	if target > fi.Size() {
		target = fi.Size()
	}
	if _, err := f.file.Seek(target, io.SeekStart); err != nil {
		return err
	}
	f.pos = target
	f.scanned = target
	return nil
}

// run is the whole follower: read what is there, wait for more, react to
// what the wait reports. It is the only goroutine that touches the file,
// the reader, the watcher or pos.
func (f *Follower) run() {
	defer close(f.finished)
	defer close(f.lines)
	defer f.cleanup()

	for {
		// Stop must always be able to end this loop, whatever state the
		// file is in. Every blocking wait below selects on done, but this
		// check means even a classify that keeps saying "there is more"
		// cannot outlive a Stop.
		select {
		case <-f.done:
			return
		default:
		}
		f.rounds.Add(1)

		alive, err := f.readAvailable()
		if err != nil {
			f.setErr(err)
			return
		}
		if !alive {
			// Stopped mid-send.
			return
		}

		switch kind, err := f.waitForChange(); kind {
		case changeStopped:
			return
		case changeError:
			f.setErr(err)
			return
		case changeModified:
			// Loop round and read it.
		case changeTruncated:
			if err := f.reopen(); err != nil {
				f.setErr(err)
				return
			}
		case changeGone:
			if !f.opts.ReOpen {
				f.setErr(ErrFileRemoved)
				return
			}
			if !f.waitForFile() {
				return
			}
			if err := f.reopen(); err != nil {
				f.setErr(err)
				return
			}
		}
	}
}

// readAvailable delivers every complete line from pos onwards, and leaves
// the read position at the start of any trailing partial line so that line
// is delivered once, whole, when its newline arrives. It reports false if
// the Follower was stopped while a send was in flight.
func (f *Follower) readAvailable() (bool, error) {
	for {
		s, err := f.reader.ReadString('\n')
		switch {
		case err == nil:
			start := f.pos
			f.pos += int64(len(s))
			if !f.send(Line{Text: strings.TrimRight(s, "\n"), Offset: start}) {
				return false, nil
			}
		case err == io.EOF:
			// scanned is how far into the file we have *looked*, which
			// is past pos exactly when a trailing fragment is sitting
			// there unterminated. Keeping the two apart is what stops an
			// unfinished last line from reading as "the file has grown"
			// forever -- that would spin, reading the same fragment and
			// rewinding, for as long as the writer took to finish the
			// line (or, for a task whose final line never gets a
			// newline, until the follower was stopped).
			f.scanned = f.pos + int64(len(s))
			if len(s) > 0 {
				// A line the writer has not finished. Rewind to where
				// it started; bufio has to be reset because the file
				// handle moved under it.
				if _, serr := f.file.Seek(f.pos, io.SeekStart); serr != nil {
					return false, serr
				}
				f.reader.Reset(f.file)
			}
			return true, nil
		default:
			return false, err
		}
	}
}

// send blocks until the consumer takes the line or the Follower is
// stopped. Selecting on done is what makes Stop always able to make
// progress against a consumer that walked away mid-stream — the property
// lib/tailed_file's subscriber Stop and server's log handlers rely on
// (turtlemonvh/blanket#123, #130).
func (f *Follower) send(l Line) bool {
	select {
	case f.lines <- l:
		return true
	case <-f.done:
		return false
	}
}

type changeKind int

const (
	changeNone changeKind = iota
	changeModified
	changeTruncated
	changeGone
	changeStopped
	changeError
)

// waitForChange blocks until the file has something new to say. It stats
// before every wait, so a write that landed between the EOF read and the
// wait can't be missed, and stats again after every wakeup: an event is a
// hint, the stat is the answer.
func (f *Follower) waitForChange() (changeKind, error) {
	for {
		kind, err := f.classify()
		if kind != changeNone {
			return kind, err
		}
		if !f.awaitWakeup() {
			return changeStopped, nil
		}
	}
}

// classify compares the file against where the reader has got to.
//
// The size questions are asked of the *open handle*, not of the path. That
// is the difference between this and hpcloud/tail's polling watcher, and
// it matters twice: a truncation is then detected exactly (it is our own
// inode that shrank, not "something at that name is smaller than it was"),
// and a path that has been replaced by a larger file can never be reported
// as "modified" when our own handle has nothing more to give — which would
// be a stat-read-stat spin, not a wait.
//
// The path is only consulted once our file is fully read, so the old
// file's last lines are always delivered before the follower moves on
// from it.
//
// The comparison is against scanned, not pos: a file whose last line has
// no newline yet has bytes we have read and are holding back, and those
// bytes are not "new content" the next time round.
func (f *Follower) classify() (changeKind, error) {
	fi, err := f.file.Stat()
	if err != nil {
		return changeError, err
	}
	switch {
	case fi.Size() > f.scanned:
		return changeModified, nil
	case fi.Size() < f.scanned:
		return changeTruncated, nil
	}

	pathFi, err := os.Stat(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return changeGone, nil
		}
		return changeError, err
	}
	// A different inode under the same name: the file was rotated or
	// replaced. Only ReOpen cares — that is exactly the `tail -f` (follow
	// this file) versus `tail -F` (follow this name) distinction, and
	// leaving the identity check to ReOpen also keeps the platforms where
	// os.SameFile is least well trodden off blanket's own code path, since
	// nothing in blanket sets it.
	if f.opts.ReOpen && !os.SameFile(pathFi, fi) {
		return changeGone, nil
	}
	return changeNone, nil
}

// awaitWakeup blocks for one hint that the file may have changed, and
// reports false if the Follower was stopped instead.
func (f *Follower) awaitWakeup() bool {
	for {
		select {
		case <-f.done:
			return false
		case ev, ok := <-f.events:
			if !ok {
				// Watcher closed under us; the ticker carries on.
				f.events, f.errs = nil, nil
				continue
			}
			if f.coalesce(f.concerns(ev)) {
				return true
			}
		case _, ok := <-f.errs:
			if !ok {
				f.events, f.errs = nil, nil
				continue
			}
			// A watcher error (a full kernel queue, most likely) is not
			// fatal: something may well have been missed, so treat it as
			// a reason to go and look.
			return true
		case <-f.ticker.C:
			return true
		}
	}
}

// concerns reports whether an event on the watched directory is about the
// file being followed.
func (f *Follower) concerns(ev fsnotify.Event) bool {
	return filepath.Clean(ev.Name) == filepath.Clean(f.path)
}

// coalesce drains whatever else the watcher has already queued, so a burst
// of writes costs one stat rather than one per event.
func (f *Follower) coalesce(matched bool) bool {
	for {
		select {
		case ev, ok := <-f.events:
			if !ok {
				f.events, f.errs = nil, nil
				return matched
			}
			matched = matched || f.concerns(ev)
		default:
			return matched
		}
	}
}

// waitForFile blocks until the path exists again (ReOpen only). Reports
// false if the Follower was stopped first.
func (f *Follower) waitForFile() bool {
	for {
		if _, err := os.Stat(f.path); err == nil {
			return true
		}
		if !f.awaitWakeup() {
			return false
		}
	}
}

// reopen swaps in the file that is at the path now and starts from its
// first byte — the right thing for both of the cases that reach it, a
// truncation (the old bytes are gone) and a replacement (they were never
// ours).
func (f *Follower) reopen() error {
	if f.file != nil {
		f.file.Close()
	}
	file, err := os.Open(f.path)
	if err != nil {
		return err
	}
	f.file = file
	f.pos = 0
	f.scanned = 0
	f.reader.Reset(file)
	return nil
}

// cleanup releases everything the run goroutine owns. Closing the fsnotify
// watcher is what removes the inotify watch — the job hpcloud/tail exposed
// as Tail.Cleanup() and left to the caller to remember.
func (f *Follower) cleanup() {
	f.ticker.Stop()
	if f.watcher != nil {
		f.watcher.Close()
		f.watcher = nil
	}
	if f.file != nil {
		f.file.Close()
		f.file = nil
	}
}
