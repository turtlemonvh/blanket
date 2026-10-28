package command

/*

Tests for the "is the server actually on the installed version?" check that
runs before `blanket upgrade` says "nothing to do" (turtlemonvh/blanket#264).

The installed CLI is the version a swap put on disk, so after a restart that
failed post-swap -- or after --no-restart -- comparing the CLI's own version
with the target says "already running" while the old server is still the
one on the port.

*/

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/upgrade"
)

// startFakeVersion serves GET /version with the given banner, and GET
// /ops/restart/status with no restart in flight, and returns the port.
func startFakeVersion(t *testing.T, bannerText string) int {
	return startFakeVersionInRestart(t, bannerText, "IDLE")
}

// startFakeVersionInRestart is startFakeVersion with the restart record in
// the given state.
func startFakeVersionInRestart(t *testing.T, bannerText, restartState string) int {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": bannerText, "name": "blanket"})
	})
	mux.HandleFunc("/ops/restart/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"restart": map[string]string{"state": restartState}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return portOf(t, srv.URL)
}

func newVersionCheckResult(t *testing.T) *upgradeResult {
	t.Helper()
	return &upgradeResult{
		BinaryPath:  "/opt/blanket/blanket",
		JournalPath: filepath.Join(t.TempDir(), upgrade.JournalName),
	}
}

func writeJournalInState(t *testing.T, p, state string) {
	t.Helper()
	j := upgrade.NewJournal("abc", "upgrade", time.Now())
	j.State = state
	require.NoError(t, j.Save(p))
}

func TestServerStillOld_SameVersionIsNothingToDo(t *testing.T) {
	port := startFakeVersion(t, banner("v9.9.1"))
	res := newVersionCheckResult(t)

	assert.False(t, serverStillOld(res, port, "v9.9.1"))
	assert.Equal(t, "v9.9.1", res.ServerVersion)
	assert.Zero(t, res.ExitCode)
}

func TestServerStillOld_NoServerIsNothingToDo(t *testing.T) {
	res := newVersionCheckResult(t)

	assert.False(t, serverStillOld(res, freePort(t), "v9.9.1"))
	assert.Empty(t, res.ServerVersion)
}

// A dev build's banner names no version; that is unknown, not a mismatch,
// the same as the post-upgrade verification treats it.
func TestServerStillOld_DevBuildIsUnknown(t *testing.T) {
	port := startFakeVersion(t, "blanket (dev) branch=master commit=abc")
	res := newVersionCheckResult(t)

	assert.False(t, serverStillOld(res, port, "v9.9.1"))
	assert.Empty(t, res.ServerVersion)
}

func TestServerStillOld_DifferentVersionWithoutJournalSaysRestart(t *testing.T) {
	port := startFakeVersion(t, banner("v9.9.0"))
	res := newVersionCheckResult(t)

	require.True(t, serverStillOld(res, port, "v9.9.1"))
	assert.Equal(t, ExitStagedOnly, res.ExitCode)
	assert.Equal(t, "v9.9.0", res.ServerVersion)
	assert.Contains(t, res.Message, "blanket v9.9.1 is installed at /opt/blanket/blanket, but the server on :")
	assert.Contains(t, res.Message, "is still running v9.9.0.")
	assert.Contains(t, res.Message, "supervisor")
	assert.NotContains(t, res.Message, "--resume")
}

func TestServerStillOld_UnfinishedJournalPointsAtResume(t *testing.T) {
	for _, state := range []string{upgrade.JournalFailed, upgrade.JournalSwapped, upgrade.JournalStaged} {
		t.Run(state, func(t *testing.T) {
			port := startFakeVersionInRestart(t, banner("v9.9.0"), "DRAINING")
			res := newVersionCheckResult(t)
			writeJournalInState(t, res.JournalPath, state)

			require.True(t, serverStillOld(res, port, "v9.9.1"))
			assert.Equal(t, ExitStagedOnly, res.ExitCode)
			assert.Contains(t, res.Message, "blanket upgrade --resume --yes")
		})
	}
}

func TestServerStillOld_FinishedJournalSaysRestart(t *testing.T) {
	for _, state := range []string{upgrade.JournalVerified, upgrade.JournalAborted} {
		t.Run(state, func(t *testing.T) {
			port := startFakeVersion(t, banner("v9.9.0"))
			res := newVersionCheckResult(t)
			writeJournalInState(t, res.JournalPath, state)

			require.True(t, serverStillOld(res, port, "v9.9.1"))
			assert.Contains(t, res.Message, "supervisor")
			assert.NotContains(t, res.Message, "--resume")
		})
	}
}

// With no restart in flight (the watchdog aborted it, or --no-restart never
// began one) --resume refuses (turtlemonvh/blanket#265), so the hint is the
// supervisor even when the journal is unfinished.
func TestServerStillOld_UnfinishedJournalWithNoRestartSaysRestart(t *testing.T) {
	port := startFakeVersion(t, banner("v9.9.0"))
	res := newVersionCheckResult(t)
	writeJournalInState(t, res.JournalPath, upgrade.JournalFailed)

	require.True(t, serverStillOld(res, port, "v9.9.1"))
	assert.Contains(t, res.Message, "supervisor")
	assert.NotContains(t, res.Message, "--resume")
}
