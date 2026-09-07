package command

/*

The update notice (turtlemonvh/blanket#23 phase 6).

Everything about how this works is in lib/upgrade/notice.go; what lives
here is *when* it is allowed to say anything. Three gates, and each one
exists because a notice that fires through it would be worse than no
notice at all:

  - `upgrade.checkForUpdates: false` turns it off entirely, for an install
    that would rather its CLI never mentioned the internet;
  - `--json` and any other machine-readable output turns it off, because a
    friendly line prepended to a document somebody is piping into `jq` is a
    parse error;
  - a non-terminal stdout turns it off, which covers `blanket ps | wc -l`
    and every script anyone has ever written around the CLI.

The refresh runs at most once a day, in a goroutine with a 250ms budget,
and writes a cache the *next* invocation reads. The command never waits on
the network to produce its own output.

*/

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/upgrade"
)

// StartUpdateNotice prints a cached update notice if there is one to
// print, kicks off a background refresh if one is due, and returns a
// function to call before the process exits.
//
// The returned function is always safe to call and never returns an
// error: a failed version check must not be able to affect the exit status
// of a command that only wanted to list tasks.
func StartUpdateNotice() func() {
	noop := func() {}
	if !viper.GetBool("upgrade.checkForUpdates") || upgradeConf.JSON || rollbackConf.JSON || !stdoutIsTerminal() {
		return noop
	}

	n := &upgrade.Notifier{
		Path: upgradeNoticePath(),
		Fetch: func(ctx context.Context) (string, error) {
			c := &upgrade.Client{
				BaseURL: viper.GetString("upgrade.releasesBaseURL"),
				Repo:    viper.GetString("upgrade.repo"),
				HTTP:    httpx.Client(),
			}
			rel, err := c.Latest(ctx)
			if err != nil {
				return "", err
			}
			return rel.TagName, nil
		},
	}

	// Print first, refresh second. The message the user sees comes from
	// the cache an earlier run wrote, so this line is on the fast path and
	// the network is not.
	if msg := n.Message(RawVersion); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	return n.StartRefresh()
}

// stdoutIsTerminal reports whether stdout is a character device.
//
// os.Stat on the fd rather than a terminal library: blanket has no such
// dependency, and the only distinction that matters here is "somebody is
// reading this" versus "this is a pipe or a file". A pipe or a regular
// file has ModeCharDevice unset, which is exactly the case to stay quiet
// in.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
