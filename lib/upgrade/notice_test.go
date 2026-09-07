package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNoticePrintsFromCacheOnly is the constraint the whole notice design
// exists to satisfy: producing the message must not touch the network.
func TestNoticePrintsFromCacheOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), NoticeName)
	n := &Notifier{
		Path: p,
		Now:  func() time.Time { return time.Unix(1_000_000, 0) },
		Fetch: func(context.Context) (string, error) {
			t.Fatal("Message must never call Fetch")
			return "", nil
		},
	}

	// Cold cache: nothing to say, and no fetch to say it with.
	if msg := n.Message("v0.4.0"); msg != "" {
		t.Errorf("a cold cache should print nothing, got %q", msg)
	}

	if err := SaveNotice(p, NoticeCache{CheckedTs: 999_000, LatestVersion: "v0.5.0"}); err != nil {
		t.Fatal(err)
	}
	if msg := n.Message("v0.4.0"); msg == "" {
		t.Error("a cached newer version should produce a message")
	}
	// Already current, and ahead of the cache, both say nothing.
	if msg := n.Message("v0.5.0"); msg != "" {
		t.Errorf("running the latest should print nothing, got %q", msg)
	}
	if msg := n.Message("v0.6.0"); msg != "" {
		t.Errorf("running ahead of the cache should print nothing, got %q", msg)
	}
}

func TestNoticeRefreshIsAtMostDaily(t *testing.T) {
	p := filepath.Join(t.TempDir(), NoticeName)
	now := time.Unix(1_000_000, 0)
	calls := 0
	n := &Notifier{
		Path:     p,
		Interval: 24 * time.Hour,
		Now:      func() time.Time { return now },
		Fetch: func(context.Context) (string, error) {
			calls++
			return "v0.5.0", nil
		},
	}

	if !n.Due() {
		t.Fatal("a cold cache is always due")
	}
	n.StartRefresh()()
	if calls != 1 {
		t.Fatalf("want 1 fetch, got %d", calls)
	}
	if got := LoadNotice(p).LatestVersion; got != "v0.5.0" {
		t.Fatalf("cache holds %q after a refresh", got)
	}

	// An hour later, not due.
	now = now.Add(time.Hour)
	if n.Due() {
		t.Error("a refresh an hour after the last one is not due")
	}
	n.StartRefresh()()
	if calls != 1 {
		t.Errorf("StartRefresh fetched again inside the interval (%d calls)", calls)
	}

	// A day later, due again.
	now = now.Add(24 * time.Hour)
	n.StartRefresh()()
	if calls != 2 {
		t.Errorf("want 2 fetches after the interval elapsed, got %d", calls)
	}
}

// TestNoticeRefreshStampsOnFailure: without this, an offline machine
// retries on every single invocation forever, which is the pathological
// version of "at most once per day".
func TestNoticeRefreshStampsOnFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), NoticeName)
	now := time.Unix(2_000_000, 0)
	if err := SaveNotice(p, NoticeCache{CheckedTs: 1, LatestVersion: "v0.5.0"}); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{
		Path: p,
		Now:  func() time.Time { return now },
		Fetch: func(context.Context) (string, error) {
			return "", errors.New("network is unreachable")
		},
	}
	n.StartRefresh()()

	c := LoadNotice(p)
	if c.CheckedTs != now.Unix() {
		t.Errorf("a failed refresh must still stamp CheckedTs (got %d, want %d)", c.CheckedTs, now.Unix())
	}
	// And it must not erase a known-good answer: a week offline should
	// leave the notice stale, not switch it off.
	if c.LatestVersion != "v0.5.0" {
		t.Errorf("a failed refresh erased the cached version: %q", c.LatestVersion)
	}
	if c.LastError == "" {
		t.Error("the failure should be recorded")
	}
}

// TestNoticeNeverBlocks: a Fetch that hangs must cost the caller at most
// the budget, whatever the network is doing.
func TestNoticeNeverBlocks(t *testing.T) {
	// Deliberately NOT t.TempDir(): the point of this test is that the
	// refresh goroutine is abandoned rather than joined, so it may still
	// be running (and about to write the cache) when the test returns.
	// t.TempDir's cleanup would race it.
	dir, err := os.MkdirTemp("", "blanket-notice-*")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, NoticeName)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	n := &Notifier{
		Path:   p,
		Budget: 50 * time.Millisecond,
		Fetch: func(ctx context.Context) (string, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return "", ctx.Err()
		},
	}
	start := time.Now()
	n.StartRefresh()()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("a hung fetch cost the caller %s; the budget is 50ms", elapsed)
	}
}

func TestNoticeSurvivesCorruptCache(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, NoticeName)
	if err := writeFile(p, "{{{ not json"); err != nil {
		t.Fatal(err)
	}
	// A corrupted cache must never be able to break a command that only
	// wanted to list tasks.
	n := &Notifier{Path: p}
	if msg := n.Message("v0.4.0"); msg != "" {
		t.Errorf("corrupt cache produced %q", msg)
	}
	if !n.Due() {
		t.Error("a corrupt cache reads as empty, which is always due")
	}
}
