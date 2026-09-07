package follow

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Every test here runs the follower in polling mode. That is deliberate:
// polling is the only mode blanket asks for on Windows (see
// worker/tail_watch_windows.go) and the one lib/tailed_file asks for
// everywhere, and it is the mode this file's tests can assert timing
// against on any platform. The fsnotify path -- and a parity test that
// runs the same scenario both ways -- lives in follow_notify_test.go,
// which is unix-only.
const (
	testPoll    = 20 * time.Millisecond
	testTimeout = 15 * time.Second
)

func pollOpts() Options {
	return Options{Poll: true, PollInterval: testPoll}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func appendFile(t *testing.T, p, content string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append %s: %v", p, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", p, err)
	}
}

// next takes one line off the follower, or fails the test.
func next(t *testing.T, f *Follower, what string) Line {
	t.Helper()
	select {
	case l, ok := <-f.Lines():
		if !ok {
			t.Fatalf("Lines closed while waiting for %s (Err: %v)", what, f.Err())
		}
		return l
	case <-time.After(testTimeout):
		t.Fatalf("timed out waiting for %s", what)
		return Line{}
	}
}

// collectUntil reads lines until it sees sentinel, and returns everything
// including it.
func collectUntil(t *testing.T, f *Follower, sentinel string) []Line {
	t.Helper()
	var got []Line
	deadline := time.After(testTimeout)
	for {
		select {
		case l, ok := <-f.Lines():
			if !ok {
				t.Fatalf("Lines closed before %q arrived (got %v, Err: %v)", sentinel, texts(got), f.Err())
			}
			got = append(got, l)
			if l.Text == sentinel {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; got %v", sentinel, texts(got))
			return nil
		}
	}
}

func texts(ls []Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Text
	}
	return out
}

// The offset/whence model borrowed from hpcloud/tail's SeekInfo: a caller
// says exactly where to start, and the follower delivers from there. The
// two clamping rows are blanket's addition -- see Options.
func TestOpen_OffsetAndWhence(t *testing.T) {
	const content = "one\ntwo\nthree\n" // 4 + 4 + 6 = 14 bytes

	cases := []struct {
		name   string
		offset int64
		whence int
		want   string
	}{
		{"start of file", 0, io.SeekStart, "one|two|three|four"},
		{"mid-file byte offset", 4, io.SeekStart, "two|three|four"},
		{"end of file", 0, io.SeekEnd, "four"},
		{"back from the end", -6, io.SeekEnd, "three|four"},
		{"seek-current on a fresh handle is seek-start", 4, io.SeekCurrent, "two|three|four"},
		{"a negative resolved offset clamps to the start", -1000, io.SeekEnd, "one|two|three|four"},
		{"an offset past the end clamps to the end", 1000, io.SeekStart, "four"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "log", content)
			opts := pollOpts()
			opts.Offset, opts.Whence = tc.offset, tc.whence

			f, err := Open(p, opts)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer f.Stop()

			appendFile(t, p, "four\n")
			got := strings.Join(texts(collectUntil(t, f, "four")), "|")
			if got != tc.want {
				t.Errorf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpen_InvalidWhence(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "x\n")
	opts := pollOpts()
	opts.Whence = 99
	if f, err := Open(p, opts); err == nil {
		f.Stop()
		t.Fatal("Open with a bogus Whence should fail")
	}
}

// A missing file is an error, not an empty follower: every caller in
// blanket already knows the file exists, so this is a bug report rather
// than a state to sit in.
func TestOpen_MissingFile(t *testing.T) {
	if f, err := Open(filepath.Join(t.TempDir(), "nope"), pollOpts()); err == nil {
		f.Stop()
		t.Fatal("Open on a missing file should fail")
	}
}

// The guarantee lib/tailed_file's ReplayAndFollow and
// worker.ExecOutput's residual read are both built on: an unterminated
// final line is not a line yet. It is held until its newline arrives, and
// then delivered once, whole.
func TestFollower_HoldsPartialLineUntilNewline(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "done\nhalf")

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the complete line"); l.Text != "done" || l.Offset != 0 {
		t.Fatalf("first line = %+v, want {done 0}", l)
	}

	// Nothing more may arrive while the fragment is unterminated.
	select {
	case l, ok := <-f.Lines():
		t.Fatalf("got %q (open=%v) while the last line was still unterminated", l.Text, ok)
	case <-time.After(10 * testPoll):
	}

	appendFile(t, p, "-way\n")
	l := next(t, f, "the completed line")
	if l.Text != "half-way" {
		t.Errorf("line = %q, want the whole line %q", l.Text, "half-way")
	}
	if l.Offset != int64(len("done\n")) {
		t.Errorf("offset = %d, want %d -- the start of the line, not of the fragment's completion",
			l.Offset, len("done\n"))
	}
}

// The bug this is the regression test for (found by
// TestProcessOne_RecordsCombinedLog hanging): a file whose last line has
// no newline has bytes past the last delivered line forever, so comparing
// the file's size against the *delivered* position said "it grew" on
// every single pass. The follower ran the read/rewind loop flat out --
// and ran it in the one place that did not check for a stop, so Stop
// never returned. A task ending in `printf 'no-newline'` is exactly that
// file, and blanket's own tests write one.
func TestFollower_DoesNotSpinOnAnUnterminatedLastLine(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "done\nno-newline")

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l := next(t, f, "the complete line"); l.Text != "done" {
		t.Fatalf("line = %q, want %q", l.Text, "done")
	}

	// Long enough that a spinning follower would rack up thousands of
	// rounds, and long enough for a handful of legitimate poll ticks.
	time.Sleep(40 * testPoll)
	if rounds := f.rounds.Load(); rounds > 20 {
		t.Errorf("%d passes round the read loop in %v -- the follower is spinning on the unterminated line",
			rounds, 40*testPoll)
	}

	// And it must still be stoppable: this is where the spin actually
	// hurt, because the loop it span in never looked at done.
	stopped := make(chan struct{})
	go func() {
		f.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned on a follower holding an unterminated last line")
	}
}

// Line.Offset is the byte offset of the line's first byte. worker's
// combined log turns it back into "how much of this file have I
// recorded", so it has to be exact for multi-byte content too.
func TestFollower_LineOffsets(t *testing.T) {
	const content = "a\nbb\nccc\n"
	p := writeFile(t, t.TempDir(), "log", content)

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	want := []Line{{Text: "a", Offset: 0}, {Text: "bb", Offset: 2}, {Text: "ccc", Offset: 5}}
	for _, w := range want {
		got := next(t, f, w.Text)
		if got.Text != w.Text || got.Offset != w.Offset {
			t.Errorf("line = {%q %d}, want {%q %d}", got.Text, got.Offset, w.Text, w.Offset)
		}
		if got.Offset+int64(len(got.Text))+1 > int64(len(content)) {
			t.Errorf("offset %d + len + 1 runs past the file", got.Offset)
		}
	}
}

// hpcloud/tail trimmed "\n" and nothing else, and blanket's stored log
// text has always matched that. A CRLF file keeps its "\r".
func TestFollower_TrimsOnlyTheNewline(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "windows\r\n")

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the CRLF line"); l.Text != "windows\r" {
		t.Errorf("line = %q, want %q -- only \\n is trimmed", l.Text, "windows\r")
	}
}

// A file shorter than where we had got to has been truncated, so the
// follower starts it again from the beginning (hpcloud/tail always
// reopened a truncated file, and so does this).
func TestFollower_ReopensOnTruncate(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "before\n")

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the pre-truncation line"); l.Text != "before" {
		t.Fatalf("line = %q, want %q", l.Text, "before")
	}

	if err := os.Truncate(p, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Let the follower notice the truncation before the file grows again.
	// Detecting "smaller than where I was" is inherently a race against a
	// writer that truncates and immediately rewrites -- it was for
	// hpcloud/tail's polling watcher too -- and this test is about the
	// reopen, not about winning that race.
	time.Sleep(20 * testPoll)

	appendFile(t, p, "after\n")
	l := next(t, f, "the post-truncation line")
	if l.Text != "after" {
		t.Errorf("line = %q, want %q", l.Text, "after")
	}
	if l.Offset != 0 {
		t.Errorf("offset = %d, want 0 -- a reopened file starts again at its first byte", l.Offset)
	}
}

// ReOpen follows the *name*: the file being renamed away and a new one
// taking its place is a pause, not the end of the stream.
func TestFollower_ReOpenFollowsTheName(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "log", "first\n")

	opts := pollOpts()
	opts.ReOpen = true
	f, err := Open(p, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the pre-rename line"); l.Text != "first" {
		t.Fatalf("line = %q, want %q", l.Text, "first")
	}

	if err := os.Rename(p, filepath.Join(dir, "log.1")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	time.Sleep(10 * testPoll)
	writeFile(t, dir, "log", "second\n")

	if l := next(t, f, "the post-rename line"); l.Text != "second" {
		t.Errorf("line = %q, want %q", l.Text, "second")
	}
}

// Without ReOpen, a file that goes away ends the stream -- and says so,
// rather than closing Lines as if it had simply been stopped.
func TestFollower_RemovedFileWithoutReOpen(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "log", "only\n")

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the only line"); l.Text != "only" {
		t.Fatalf("line = %q, want %q", l.Text, "only")
	}
	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	select {
	case _, ok := <-f.Lines():
		if ok {
			t.Fatal("got a line from a removed file")
		}
	case <-time.After(testTimeout):
		t.Fatal("Lines stayed open after the file was removed")
	}
	if err := f.Err(); err != ErrFileRemoved {
		t.Errorf("Err = %v, want ErrFileRemoved", err)
	}
}

// Stop has to be able to break a send that nobody is receiving. Lines is
// unbuffered, so a follower with a full file and no consumer is parked
// mid-send -- exactly where lib/tailed_file's subscriber Stop and the
// server's log handlers need it to be interruptible
// (turtlemonvh/blanket#123, #130).
func TestFollower_StopWhileNobodyReads(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	p := writeFile(t, t.TempDir(), "log", b.String())

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Give the follower time to park on the very first send.
	time.Sleep(10 * testPoll)

	stopped := make(chan error, 1)
	go func() { stopped <- f.Stop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Stop returned %v, want nil for a follower that was simply stopped", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Stop blocked against a consumer that never read Lines")
	}

	// Lines must be closed by the time Stop returns.
	select {
	case _, ok := <-f.Lines():
		if ok {
			// One line may have been in flight; the channel must still close.
			if _, ok := <-f.Lines(); ok {
				t.Fatal("Lines still delivering after Stop returned")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Lines was not closed by the time Stop returned")
	}

	// Idempotent: a defer plus an explicit stop is the normal shape.
	if err := f.Stop(); err != nil {
		t.Errorf("second Stop returned %v", err)
	}
}

// Stopping a follower must leave nothing behind: not its own goroutine,
// and not fsnotify's (whose watch this package closes on the way out --
// the job hpcloud/tail left to a separate Cleanup() call).
func TestFollower_NoGoroutineLeak(t *testing.T) {
	dir := t.TempDir()

	// One warm-up round so any lazily-created runtime/fsnotify goroutine
	// is counted in the baseline rather than as a leak.
	runRound := func(opts Options) {
		p := writeFile(t, dir, fmt.Sprintf("warm-%d", time.Now().UnixNano()), "a\nb\n")
		f, err := Open(p, opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		<-f.Lines()
		if err := f.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}
	notify := Options{PollInterval: testPoll}
	runRound(pollOpts())
	runRound(notify)

	settle(t)
	baseline := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		runRound(pollOpts())
		runRound(notify)
	}

	settle(t)
	if got := runtime.NumGoroutine(); got > baseline {
		t.Errorf("goroutines: %d after 40 open/stop rounds, baseline %d", got, baseline)
	}
}

// settle waits for goroutines that are on their way out (fsnotify's
// closes asynchronously) to actually finish.
func settle(t *testing.T) {
	t.Helper()
	start := runtime.NumGoroutine()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		runtime.Gosched()
		now := runtime.NumGoroutine()
		if now == start {
			return
		}
		start = now
	}
}

// A follower keeps up with a writer that is still going: this is the
// live-log case, and it must not stall at the first EOF.
func TestFollower_KeepsUpWithAWriter(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "")

	f, err := Open(p, pollOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	// The writer runs in its own goroutine, so it reports failures back
	// rather than calling t.Fatal off the test goroutine.
	const n = 25
	writeErr := make(chan error, 1)
	go func() {
		defer close(writeErr)
		for i := 0; i < n; i++ {
			w, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0644)
			if err == nil {
				_, err = fmt.Fprintf(w, "line %d\n", i)
				w.Close()
			}
			if err != nil {
				writeErr <- err
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	for i := 0; i < n; i++ {
		want := fmt.Sprintf("line %d", i)
		if l := next(t, f, want); l.Text != want {
			t.Fatalf("line %d = %q, want %q", i, l.Text, want)
		}
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("writer: %v", err)
	}
}
