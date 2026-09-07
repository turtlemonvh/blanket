package upgrade

/*

The update notice (turtlemonvh/blanket#23 phase 6).

The requirement is stated as a constraint rather than a feature: *this can
never slow the CLI down*. `blanket ps` is a command people run in a loop
and pipe into other things, and a version check that added a network round
trip to it would be a regression dressed as a courtesy — the kind that gets
noticed as "blanket got slow" long before anyone connects it to the
notice.

So the design is: **print from a file, refresh in the background, never
read the network on the path that produces output.**

  - The message comes from an on-disk cache written by an *earlier* run. A
    cold cache prints nothing at all rather than fetching one.
  - The refresh runs at most once per Interval (a day), in a goroutine with
    a hard Budget (250ms). It writes the cache for the *next* invocation.
  - The command never waits on the network. At exit it gives the refresh up
    to the same Budget to land, which is a bound the network cannot exceed
    — an unreachable GitHub costs 250ms of a run that has already printed
    its output, and a firewall that blackholes packets costs exactly the
    same, because the budget is a context deadline and not a timeout on a
    response.
  - The check-stamp is written even when the fetch fails. Otherwise an
    offline machine would retry on every single invocation forever, which
    is the pathological version of "at most once per day".

Off under `--json` or when stdout is not a terminal (the caller decides;
see command/notice.go), and off entirely with `upgrade.checkForUpdates:
false`.

*/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// NoticeName is the cache file inside the upgrade state directory.
const NoticeName = "notice.json"

const (
	// DefaultNoticeInterval is how often the cache is refreshed.
	DefaultNoticeInterval = 24 * time.Hour
	// DefaultNoticeBudget is the hard cap on the background refresh, and
	// therefore on how much a run can ever pay for the notice.
	DefaultNoticeBudget = 250 * time.Millisecond
)

// NoticeCache is the on-disk cache.
type NoticeCache struct {
	// CheckedTs is when a refresh last *ran*, successfully or not.
	CheckedTs int64 `json:"checkedTs"`
	// LatestVersion is the newest release tag seen, "" if never seen one.
	LatestVersion string `json:"latestVersion,omitempty"`
	// LastError records why the last refresh failed, for `blanket upgrade
	// --check` to mention rather than for the notice to print.
	LastError string `json:"lastError,omitempty"`
}

// LoadNotice reads the cache. A missing or unreadable file is an empty
// cache, not an error: a corrupted cache must never be able to break a
// command that only wanted to list tasks.
func LoadNotice(p string) NoticeCache {
	b, err := os.ReadFile(p)
	if err != nil {
		return NoticeCache{}
	}
	var c NoticeCache
	if err := json.Unmarshal(b, &c); err != nil {
		return NoticeCache{}
	}
	return c
}

// SaveNotice writes the cache atomically.
func SaveNotice(p string, c NoticeCache) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".notice-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// Notifier is the whole notice mechanism.
//
// Now is injectable so the "at most once per day" rule is testable without
// a sleeping test, and Fetch is injectable so the tests never touch a
// network. Both default sensibly for production use.
type Notifier struct {
	Path     string
	Now      func() time.Time
	Interval time.Duration
	Budget   time.Duration
	// Fetch returns the latest release tag. Called only from the
	// background goroutine, never on the printing path.
	Fetch func(ctx context.Context) (string, error)
}

func (n *Notifier) now() time.Time {
	if n.Now == nil {
		return time.Now()
	}
	return n.Now()
}

func (n *Notifier) interval() time.Duration {
	if n.Interval <= 0 {
		return DefaultNoticeInterval
	}
	return n.Interval
}

func (n *Notifier) budget() time.Duration {
	if n.Budget <= 0 {
		return DefaultNoticeBudget
	}
	return n.Budget
}

// Message returns the line to print, or "" for nothing to say. It reads
// the cache and never the network.
func (n *Notifier) Message(currentVersion string) string {
	c := LoadNotice(n.Path)
	if c.LatestVersion == "" || !IsNewer(currentVersion, c.LatestVersion) {
		return ""
	}
	return fmt.Sprintf("A newer blanket is available: %s (you have %s). Run `blanket upgrade` to install it.",
		c.LatestVersion, displayVersion(currentVersion))
}

func displayVersion(v string) string {
	if v == "" {
		return "a development build"
	}
	return v
}

// Due reports whether a refresh should run now.
func (n *Notifier) Due() bool {
	c := LoadNotice(n.Path)
	if c.CheckedTs == 0 {
		return true
	}
	return n.now().Sub(time.Unix(c.CheckedTs, 0)) >= n.interval()
}

// StartRefresh kicks off the background refresh if one is due and returns
// a function that gives it up to Budget to finish.
//
// The returned function is safe to call even when nothing was started, so
// the caller has no branch to get wrong. It never returns an error: a
// failed version check is not something a `blanket ps` invocation should
// be able to report, let alone fail on.
func (n *Notifier) StartRefresh() (wait func()) {
	if n.Fetch == nil || !n.Due() {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), n.budget())
		defer cancel()

		c := NoticeCache{CheckedTs: n.now().Unix()}
		// Carry the previous answer forward: a failed refresh must not
		// erase a known-good latest version, or a week offline would turn
		// the notice off rather than leave it stale.
		if prev := LoadNotice(n.Path); prev.LatestVersion != "" {
			c.LatestVersion = prev.LatestVersion
		}

		tag, err := n.Fetch(ctx)
		switch {
		case err != nil:
			c.LastError = err.Error()
		case tag != "":
			c.LatestVersion = tag
		}
		_ = SaveNotice(n.Path, c)
	}()

	return func() {
		select {
		case <-done:
		case <-time.After(n.budget()):
			// Abandoned, not cancelled: the goroutine's own context
			// deadline stops it, and the process is about to exit anyway.
		}
	}
}

// ErrNoticeDisabled is returned by helpers that are asked to act while the
// notice is switched off, so a caller can tell "nothing to say" from
// "not allowed to look".
var ErrNoticeDisabled = errors.New("update checks are disabled (upgrade.checkForUpdates)")
