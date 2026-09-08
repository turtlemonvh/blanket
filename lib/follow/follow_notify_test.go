//go:build !windows

// The fsnotify half of the suite. Unix-only for the same reason blanket
// itself never asks for fsnotify on Windows (worker/tail_watch_windows.go):
// the Windows CI job runs this package too, and there it should exercise
// exactly the mode the product uses there -- polling, covered by
// follow_test.go. Nothing here is inotify-specific beyond "not the
// ticker": the point is that the notify path delivers the same lines.

package follow

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func notifyOpts() Options {
	// A long PollInterval so the fallback ticker cannot be what makes
	// these pass: if fsnotify were not doing the work, every wait would
	// take a second and the parity test's timings would say so.
	return Options{Poll: false, PollInterval: time.Second}
}

// The notify path has to satisfy every promise the polling path does.
func TestFollower_Notify_DeliversAppendedLines(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "one\n")

	f, err := Open(p, notifyOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the existing line"); l.Text != "one" {
		t.Fatalf("line = %q, want %q", l.Text, "one")
	}

	// Well under notifyFallbackPoll, so only a real event can deliver this
	// in time.
	deadline := time.After(500 * time.Millisecond)
	appendFile(t, p, "two\n")
	select {
	case l, ok := <-f.Lines():
		if !ok {
			t.Fatalf("Lines closed (Err: %v)", f.Err())
		}
		if l.Text != "two" {
			t.Errorf("line = %q, want %q", l.Text, "two")
		}
	case <-deadline:
		t.Fatal("fsnotify did not deliver an appended line within 500ms; the fallback ticker was doing the work")
	}
}

func TestFollower_Notify_HoldsPartialLineUntilNewline(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "done\nhalf")

	f, err := Open(p, notifyOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the complete line"); l.Text != "done" {
		t.Fatalf("line = %q, want %q", l.Text, "done")
	}
	appendFile(t, p, "-way\n")
	if l := next(t, f, "the completed line"); l.Text != "half-way" {
		t.Errorf("line = %q, want %q", l.Text, "half-way")
	}
}

// Parity: the same scenario, run through fsnotify and through the poller,
// must produce byte-identical lines and offsets. The wakeup mechanism is
// an implementation detail of *when* a line is delivered, never of what
// is delivered -- which is what lets lib/tailed_file poll and the worker
// use notify without their log text diverging.
func TestFollower_NotifyAndPollParity(t *testing.T) {
	scenario := func(t *testing.T, opts Options) []Line {
		t.Helper()
		dir := t.TempDir()
		p := writeFile(t, dir, "log", "alpha\nbeta\npartial")

		f, err := Open(p, opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer f.Stop()

		var got []Line
		// alpha, beta -- "partial" is held back.
		got = append(got, next(t, f, "alpha"), next(t, f, "beta"))

		// Complete the held line, then append a burst, then truncate and
		// start over.
		appendFile(t, p, "-line\n")
		got = append(got, next(t, f, "the completed line"))

		var b strings.Builder
		for i := 0; i < 5; i++ {
			fmt.Fprintf(&b, "burst %d\n", i)
		}
		appendFile(t, p, b.String())
		for i := 0; i < 5; i++ {
			got = append(got, next(t, f, fmt.Sprintf("burst %d", i)))
		}

		if err := os.Truncate(p, 0); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		time.Sleep(400 * time.Millisecond)
		appendFile(t, p, "reborn\n")
		got = append(got, next(t, f, "the post-truncation line"))

		return got
	}

	var notify, poll []Line
	t.Run("fsnotify", func(t *testing.T) { notify = scenario(t, notifyOpts()) })
	t.Run("polling", func(t *testing.T) { poll = scenario(t, pollOpts()) })

	if len(notify) != len(poll) {
		t.Fatalf("fsnotify delivered %d lines, polling %d", len(notify), len(poll))
	}
	for i := range notify {
		if notify[i] != poll[i] {
			t.Errorf("line %d: fsnotify %+v, polling %+v", i, notify[i], poll[i])
		}
	}
}

// fsnotify watches the file's parent directory, so an unrelated file
// churning next to ours must not confuse the follower (or make it spin).
func TestFollower_Notify_IgnoresSiblingFiles(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "log", "mine\n")

	f, err := Open(p, notifyOpts())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Stop()

	if l := next(t, f, "the existing line"); l.Text != "mine" {
		t.Fatalf("line = %q, want %q", l.Text, "mine")
	}

	for i := 0; i < 20; i++ {
		writeFile(t, dir, fmt.Sprintf("noise-%d", i), "not mine\n")
	}
	select {
	case l, ok := <-f.Lines():
		t.Fatalf("got %+v (open=%v) from a sibling file's events", l, ok)
	case <-time.After(300 * time.Millisecond):
	}

	appendFile(t, p, "still mine\n")
	if l := next(t, f, "our own appended line"); l.Text != "still mine" {
		t.Errorf("line = %q, want %q", l.Text, "still mine")
	}
}
