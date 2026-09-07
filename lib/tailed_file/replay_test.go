package tailed_file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	f.Close()
}

func nextLine(t *testing.T, rt *ReplayTail, what string) string {
	t.Helper()
	select {
	case line, ok := <-rt.Lines:
		if !ok {
			t.Fatalf("tail closed while waiting for %s", what)
		}
		return line
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return ""
	}
}

// The contract the UI's combined log stream depends on: everything already
// in the file comes back as history, everything appended after comes down
// the channel, and the byte offset between the two is exact -- no line is
// dropped or delivered twice at the seam.
func TestReplayAndFollow_HistoryThenLive(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "one\ntwo\nthree\n")

	rt, err := ReplayAndFollow(p, 0)
	if err != nil {
		t.Fatalf("ReplayAndFollow: %v", err)
	}
	defer rt.Stop()

	if got := strings.Join(rt.History, "|"); got != "one|two|three" {
		t.Errorf("history = %q, want the whole file", got)
	}
	if rt.Truncated {
		t.Error("a file inside the cap should not report truncation")
	}
	if rt.Offset != int64(len("one\ntwo\nthree\n")) {
		t.Errorf("offset = %d, want the end of the last complete line", rt.Offset)
	}

	appendFile(t, p, "four\n")
	if got := nextLine(t, rt, "the appended line"); got != "four" {
		t.Errorf("live line = %q, want %q", got, "four")
	}
}

// A trailing fragment is a line the writer hasn't finished. It is not
// history -- it belongs to the tailer, so it arrives once, whole.
func TestReplayAndFollow_PartialFinalLine(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "done\nhalf")

	rt, err := ReplayAndFollow(p, 0)
	if err != nil {
		t.Fatalf("ReplayAndFollow: %v", err)
	}
	defer rt.Stop()

	if got := strings.Join(rt.History, "|"); got != "done" {
		t.Errorf("history = %q, want only the complete line", got)
	}
	if rt.Offset != int64(len("done\n")) {
		t.Errorf("offset = %d, want the fragment left to the tailer", rt.Offset)
	}

	appendFile(t, p, "-way\n")
	if got := nextLine(t, rt, "the completed line"); got != "half-way" {
		t.Errorf("live line = %q, want the whole line %q", got, "half-way")
	}
}

// Past the cap only the tail is kept, and the caller is told so -- but the
// offset still points at the end of the file, so nothing is replayed twice
// just because it was dropped from the history.
func TestReplayAndFollow_CapsHistory(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	content := b.String()
	p := writeFile(t, t.TempDir(), "log", content)

	rt, err := ReplayAndFollow(p, 10)
	if err != nil {
		t.Fatalf("ReplayAndFollow: %v", err)
	}
	defer rt.Stop()

	if len(rt.History) != 10 {
		t.Fatalf("history has %d lines, want the 10-line cap", len(rt.History))
	}
	if rt.History[0] != "line 241" || rt.History[9] != "line 250" {
		t.Errorf("history = %v, want the last 10 lines", rt.History)
	}
	if !rt.Truncated {
		t.Error("a capped history should report truncation")
	}
	if rt.Offset != int64(len(content)) {
		t.Errorf("offset = %d, want %d -- the cap trims what is shown, not what is consumed",
			rt.Offset, len(content))
	}

	appendFile(t, p, "line 251\n")
	if got := nextLine(t, rt, "the appended line"); got != "line 251" {
		t.Errorf("live line = %q, want %q", got, "line 251")
	}
}

// Stop must not wedge on a consumer that walked away mid-stream: tail's
// Lines channel is unbuffered and its reader blocks on the send, so Stop
// has to unblock it rather than wait on it.
func TestReplayTail_StopWithUnreadLines(t *testing.T) {
	p := writeFile(t, t.TempDir(), "log", "one\n")

	rt, err := ReplayAndFollow(p, 0)
	if err != nil {
		t.Fatalf("ReplayAndFollow: %v", err)
	}

	// Write more than the forwarding buffer will hold without a reader.
	var b strings.Builder
	for i := 0; i < liveLineBuffer*2; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	appendFile(t, p, b.String())

	stopped := make(chan struct{})
	go func() {
		rt.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("Stop blocked on a consumer that stopped reading")
	}

	// Idempotent: a handler with a defer and an explicit stop is normal.
	rt.Stop()
}

// A file the worker hasn't created yet is an error, not an empty tail --
// the caller retries.
func TestReplayAndFollow_MissingFile(t *testing.T) {
	if _, err := ReplayAndFollow(filepath.Join(t.TempDir(), "nope"), 0); err == nil {
		t.Fatal("ReplayAndFollow on a missing file should fail")
	}
}
