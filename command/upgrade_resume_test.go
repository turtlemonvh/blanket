package command

/*

Tests for `--resume` and `--abort` over hand-built journals
(turtlemonvh/blanket#265).

The bug they pin: both commands decided from the journal's current State
alone. An upgrade that failed *after* the swap ends in FAILED, so --abort
said "nothing was installed" about a binary that was, and --resume treated
it like STAGED and refused because the staged file had been consumed by
the swap. Both now decide from the furthest state the journal reached.

The server is a stub: a small state machine over the handful of ops routes
the finish-the-restart path touches.

*/

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/upgrade"
)

// stubRestartServer answers the ops routes as an old server mid-restart
// and, once told to exec, as the replacement on the new version.
type stubRestartServer struct {
	mu          sync.Mutex
	state       string // the server's restart state
	oldVersion  string
	newVersion  string
	execed      bool
	posts       []string
	abortedResp string
}

func (s *stubRestartServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost {
		s.posts = append(s.posts, r.URL.Path)
	}
	switch r.URL.Path {
	case "/ops/restart/status":
		var st restartStatus
		if s.execed {
			st = status("new-instance", s.newVersion)
		} else {
			st = status("old-instance", s.oldVersion)
			st.Restart.State = s.state
		}
		_ = json.NewEncoder(w).Encode(st)
	case "/ops/restart/exec":
		s.execed = true
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"execMode":"exec"}`))
	case "/ops/restart/abort":
		_, _ = w.Write([]byte(s.abortedResp))
	case "/version":
		w.WriteHeader(http.StatusOK)
	default:
		// swapped / drain must not be called by a resume past them; a 409
		// makes the test fail loudly if they are.
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"unexpected call in test"}`))
	}
}

func (s *stubRestartServer) postedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.posts...)
}

func startStubRestartServer(t *testing.T, s *stubRestartServer) int {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return portOf(t, srv.URL)
}

// withUpgradeFlags sets --yes (and resets the rest) for one test.
func withUpgradeFlags(t *testing.T) {
	t.Helper()
	saved := upgradeConf
	t.Cleanup(func() { upgradeConf = saved })
	upgradeConf.Yes = true
	upgradeConf.JSON = true
}

// failedJournal writes a journal that ran through steps and then FAILED,
// the way finishUpgrade/execAndVerify leave one, and returns its path.
func failedJournal(t *testing.T, port int, steps ...string) (string, *upgrade.Journal) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "blanket")
	require.NoError(t, os.WriteFile(bin, []byte("new binary"), 0o755))

	j := upgrade.NewJournal("abc", "upgrade", time.Now())
	j.FromVersion, j.ToVersion = "v0.3.0", "v0.4.0"
	j.BinaryPath = bin
	j.Port = port
	j.FromInstanceId = "old-instance"
	for _, s := range steps {
		j.Advance(s, "")
	}
	j.Advance(upgrade.JournalFailed, "the drain timed out")
	p := filepath.Join(dir, "upgrade", upgrade.JournalName)
	require.NoError(t, j.Save(p))
	return p, j
}

var stepsThroughSwap = []string{
	upgrade.JournalStaged, upgrade.JournalBackedUp, upgrade.JournalPaused,
	upgrade.JournalSwapped, upgrade.JournalSwapped,
}

func reloadJournal(t *testing.T, p string) *upgrade.Journal {
	t.Helper()
	j, err := upgrade.LoadJournal(p)
	require.NoError(t, err)
	require.NotNil(t, j)
	return j
}

// The observed failure: FAILED after SWAPPED, with the server still
// DRAINING. --resume must finish the restart rather than demand a staged
// binary, and must not repeat the swapped/drain transitions.
func TestResume_FailedAfterSwapFinishesTheRestart(t *testing.T) {
	withUpgradeFlags(t)
	stub := &stubRestartServer{state: "DRAINING", oldVersion: "v0.3.0", newVersion: "v0.4.0"}
	port := startStubRestartServer(t, stub)
	p, _ := failedJournal(t, port, stepsThroughSwap...)

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeResume(res, "blanket")

	assert.Equal(t, ExitOK, code, "error: %s", res.Error)
	assert.Equal(t, upgrade.JournalVerified, res.State)
	assert.Equal(t, []string{"/ops/restart/exec"}, stub.postedPaths(),
		"a resume past the drain should only exec")
	assert.Equal(t, upgrade.JournalVerified, reloadJournal(t, p).State)
}

// The watchdog already aborted the restart: the server is IDLE and still
// the old process. A resume must not begin a new restart; it refuses with
// exit 13 and says to restart under the supervisor.
func TestResume_FailedAfterSwapWithNoRestartInFlightRefuses(t *testing.T) {
	withUpgradeFlags(t)
	stub := &stubRestartServer{state: "IDLE", oldVersion: "v0.3.0", newVersion: "v0.4.0"}
	port := startStubRestartServer(t, stub)
	p, _ := failedJournal(t, port, stepsThroughSwap...)

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeResume(res, "blanket")

	assert.Equal(t, ExitRestartRefused, code)
	assert.Contains(t, res.Error, "no restart in flight")
	assert.Contains(t, res.Error, "restart it under its supervisor to start v0.4.0")
	assert.Empty(t, stub.postedPaths(), "a refused resume must not touch the server")
}

// A FAILED journal that never reached the swap is unchanged: it still
// needs its staged binary.
func TestResume_FailedBeforeSwapStillNeedsTheStagedBinary(t *testing.T) {
	withUpgradeFlags(t)
	p, _ := failedJournal(t, 0, upgrade.JournalStaged, upgrade.JournalBackedUp)

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeResume(res, "blanket")

	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, res.Error, "no staged binary")
}

// --abort after a failed post-swap upgrade must say the binary was left in
// place and name rollback, and warn about drained workers.
func TestAbort_FailedAfterSwapLeavesTheBinaryAndSaysSo(t *testing.T) {
	withUpgradeFlags(t)
	stub := &stubRestartServer{abortedResp: `{"aborted":{"state":"DRAINING"},"respawned":true}`}
	port := startStubRestartServer(t, stub)
	p, _ := failedJournal(t, port, stepsThroughSwap...)

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeAbort(res)

	assert.Equal(t, ExitOK, code)
	assert.NotContains(t, res.Message, "nothing was installed")
	assert.Contains(t, res.Message, "LEFT there")
	assert.Contains(t, res.Message, "blanket rollback --yes")
	assert.True(t, hasWarning(res, "PUT /worker/:id/restart"), "warnings: %v", res.Warnings)
	assert.Equal(t, upgrade.JournalAborted, reloadJournal(t, p).State)
}

// --abort before the swap still says nothing was installed, and with no
// drain in the aborted restart there is nothing to warn about.
func TestAbort_FailedBeforeSwapSaysNothingWasInstalled(t *testing.T) {
	withUpgradeFlags(t)
	stub := &stubRestartServer{abortedResp: `{"aborted":{"state":"PAUSED"},"respawned":false}`}
	port := startStubRestartServer(t, stub)
	p, _ := failedJournal(t, port, upgrade.JournalStaged, upgrade.JournalBackedUp, upgrade.JournalPaused)

	res := &upgradeResult{Action: "upgrade", JournalPath: p}
	code := runUpgradeAbort(res)

	assert.Equal(t, ExitOK, code)
	assert.Equal(t, "Aborted; nothing was installed.", res.Message)
	assert.False(t, hasWarning(res, "PUT /worker/:id/restart"), "warnings: %v", res.Warnings)
}

func TestAbortedAfterDrain(t *testing.T) {
	assert.True(t, abortedAfterDrain([]byte(`{"aborted":{"state":"DRAINING"},"respawned":true}`)))
	// An older server sends no `respawned`; the state alone decides.
	assert.True(t, abortedAfterDrain([]byte(`{"aborted":{"state":"EXECING"}}`)))
	assert.False(t, abortedAfterDrain([]byte(`{"aborted":{"state":"SWAPPED"},"respawned":false}`)))
	assert.False(t, abortedAfterDrain([]byte(`not json`)))
}

func hasWarning(res *upgradeResult, sub string) bool {
	for _, w := range res.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}
