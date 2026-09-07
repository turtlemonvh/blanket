package command

/*

Shared plumbing for `blanket upgrade` and `blanket rollback`
(turtlemonvh/blanket#23 phase 6).

Both commands are *drivers* of the phase 5 restart state machine: they
choose a binary, put it on disk, and then walk the server through
begin -> backup -> pause -> swapped -> drain -> exec -> verify over the
loopback ops endpoints. Everything in this file is the part of that job
neither command owns alone: where the driver's own state lives on disk, how
it talks to the ops endpoints, and how it decides the server came back.

There is no ops token (brief decision row 6). The CLI is a local operator:
it connects from loopback and sets the X-Blanket-Restart header, which is
the whole of what the guard asks for. See server/serve_ops.go.

*/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/upgrade"
	"github.com/turtlemonvh/blanket/server"
)

// Exit codes shared by `blanket upgrade` and `blanket rollback`.
//
// Five outcomes are distinguishable rather than collapsed into 0/1
// because the caller is usually a script in a maintenance window, and the
// difference between "there was nothing to do" and "I replaced the binary
// but the server did not come back" is the difference between going to bed
// and getting paged. Documented in docs/upgrade.md; keep the two in sync.
const (
	// ExitOK: the upgrade completed and a new server answered on the new
	// version. For --check, "an upgrade is available".
	ExitOK = 0
	// ExitError: anything unexpected.
	ExitError = 1
	// ExitUsage: the flags don't make sense, or a precondition a human
	// must fix is unmet.
	ExitUsage = 2
	// ExitNothingToDo: already at the target version. For --check, "you
	// are up to date".
	ExitNothingToDo = 10
	// ExitStagedOnly: the new binary is staged or installed, and no
	// restart was attempted (--stage-only, --no-restart, or a server that
	// was not running).
	ExitStagedOnly = 11
	// ExitVerificationFailed: a checksum did not match, or the server that
	// came back is not the one we installed. The install is left in a
	// state the message describes, and `blanket rollback` is the way out.
	ExitVerificationFailed = 12
	// ExitRestartRefused: the binary is in place but the server would not
	// restart -- a 409 from the state machine, a drain that did not
	// finish, or a restart already in flight.
	ExitRestartRefused = 13
)

// ---------------------------------------------------------------------------
// Where the driver's state lives
// ---------------------------------------------------------------------------

// upgradeStateDir is `<database dir>/upgrade` unless upgrade.stateDir says
// otherwise.
//
// Beside the database for the same reason backups are (docs/upgrade.md):
// the property that matters at 2am is that the thing you need is where you
// will look for it, and the database directory is already the one place an
// operator knows blanket keeps its state. It also means the rollback slots
// and the database backups that pair with them live under one directory,
// so "how much disk does blanket's safety net cost me?" has one answer.
func upgradeStateDir() string {
	if d := viper.GetString("upgrade.stateDir"); d != "" {
		return d
	}
	db := viper.GetString("database")
	if db == "" {
		db = "blanket.db"
	}
	abs, err := filepath.Abs(db)
	if err != nil {
		abs = db
	}
	return filepath.Join(filepath.Dir(abs), "upgrade")
}

func upgradeJournalPath() string {
	return filepath.Join(upgradeStateDir(), upgrade.JournalName)
}

func upgradeSlotsDir() string {
	return filepath.Join(upgradeStateDir(), upgrade.SlotsName)
}

func upgradeNoticePath() string {
	return filepath.Join(upgradeStateDir(), upgrade.NoticeName)
}

func upgradeSlotCount() int {
	n := viper.GetInt("upgrade.slots")
	if n < 1 {
		return upgrade.DefaultSlots
	}
	return n
}

// resolveInstalledBinary is the path an upgrade replaces: this process's
// own executable, with symlinks followed.
//
// "The binary I am running" is the right answer and not merely a
// convenient one. An operator with two blankets on the box (a packaged one
// and a hand-built one in ~/bin) upgrades the one they just typed the name
// of, which is the one that resolved.
func resolveInstalledBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("could not determine this binary's path: %w", err)
	}
	return upgrade.ResolveInstalledPath(exe)
}

// ---------------------------------------------------------------------------
// The ops endpoints
// ---------------------------------------------------------------------------

// restartStatus is the subset of GET /ops/restart/status this needs. A
// local struct rather than the server's own (unexported) response type:
// the CLI must be able to read a status document written by a *different*
// build of blanket -- that is the entire point of an upgrade -- so it
// decodes the fields it knows and ignores the rest.
type restartStatus struct {
	Restart struct {
		State      string `json:"state"`
		Id         string `json:"id"`
		Reason     string `json:"reason"`
		BackupPath string `json:"backupPath"`
	} `json:"restart"`
	Active           bool   `json:"active"`
	SpawnPaused      bool   `json:"spawnPaused"`
	ExecMode         string `json:"execMode"`
	ResolvedExecMode string `json:"resolvedExecMode"`
	DrainMode        string `json:"drainMode"`
	Supervised       bool   `json:"supervised"`
	InstanceId       string `json:"instanceId"`
	Pid              int    `json:"pid"`
	Version          string `json:"version"`
}

// opsError is an HTTP failure from a server that answered.
type opsError struct {
	Status int
	Path   string
	Body   string
}

func (e *opsError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(e.Body), &payload) == nil && payload.Error != "" {
		msg = payload.Error
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Path, e.Status, msg)
}

// errServerDown means nothing answered on the port at all — a transport
// failure, not an HTTP one. The distinction is load-bearing in both
// commands: a server that is *down* means "swap the binary and stop", and
// a server that *refused* means "do not go behind its back".
var errServerDown = errors.New("no blanket server is answering")

// opsCall makes one request to a loopback ops endpoint.
func opsCall(ctx context.Context, port int, method, path string, body []byte) ([]byte, error) {
	url := fmt.Sprintf("http://localhost:%d%s", port, path)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set(server.OpsHeader, "cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := httpx.Client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w on port %d: %v", errServerDown, port, err)
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return rb, &opsError{Status: res.StatusCode, Path: path, Body: string(rb)}
	}
	return rb, nil
}

func opsCallTimeout(port int, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return opsCall(ctx, port, method, path, body)
}

// fetchRestartStatus reads GET /ops/restart/status.
func fetchRestartStatus(port int) (*restartStatus, error) {
	b, err := opsCallTimeout(port, http.MethodGet, "/ops/restart/status", nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	var st restartStatus
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("could not read the restart status: %w", err)
	}
	return &st, nil
}

// serverAnswers reports whether anything is listening and answering
// /version on the port.
func serverAnswers(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://localhost:%d/version", port), nil)
	if err != nil {
		return false
	}
	res, err := httpx.Client().Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	return res.StatusCode == http.StatusOK
}

// statusProbeBudget is how long "is there a server?" keeps asking before
// it believes the answer is no.
//
// A server re-execing in place is not listening for a moment, and one
// instant's connection-refused is not the same claim as "no server is
// running here" -- the two lead to opposite decisions (drive the restart
// vs. swap the binary and stop). Unscaled constant through timing.Scale,
// per lib/timing's rule.
const statusProbeBudget = 3 * time.Second

// quietWindow is how long the port has to stay silent before the CLI will
// start a replacement server itself.
//
// Same gap, read from the other side: `waitForServerGone` returning true
// the first time nothing answers cannot tell a server that has exited from
// one that is between process images. Starting a second server against the
// latter leaves a process parked on the bolt lock which, minutes later at
// the *next* restart, wins the race for the port and answers as the
// version everyone thought had been replaced.
const quietWindow = 3 * time.Second

// awaitRestartStatus is fetchRestartStatus with a short retry while
// nothing answers at all.
//
// Only errServerDown is retried. A server that answered and said no -- a
// 403 from the ops guard, a 500 -- has given a real answer, and asking it
// again four more times would only delay reporting it.
func awaitRestartStatus(port int, budget time.Duration) (*restartStatus, error) {
	deadline := time.Now().Add(budget)
	for {
		st, err := fetchRestartStatus(port)
		if err == nil || !errors.Is(err, errServerDown) {
			return st, err
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitForNewServer polls until a server answers that is provably the
// replacement -- a different instance id *and* the version that was
// installed -- or the budget runs out.
//
// Both halves are load-bearing, and each on its own has been seen to pass
// something through that should not have.
//
// The instance id, not the fact that *something* answered, is what makes
// this a verification: the old server keeps serving right up until its
// listener closes, so a poll that accepted the first 200 it saw would
// routinely "verify" the process it was replacing.
//
// The version is what catches the other impostor: a *third* process. An
// abandoned server sitting on the database lock -- one the CLI started
// against a server that turned out to be re-execing, say -- is a different
// process with a different instance id, and it wins the port the moment
// the real replacement lets go of it. It answers on the old version, and
// the wait must keep waiting rather than declare that the replacement came
// back wrong. Hence `lastWrong`: if the budget runs out with nothing but
// impostors, the caller still gets to say which version answered.
//
// wantVersion is a tag ("v0.5.0"), compared against the server's banner.
// It may be empty (a development build records no version), and so may the
// banner; neither can prove anything, and the check is skipped rather than
// failed -- the same case the caller's own version check already skips.
func waitForNewServer(port int, notInstanceId, wantVersion string, budget time.Duration) (*restartStatus, error) {
	deadline := time.Now().Add(budget)
	var last error
	var lastWrong *restartStatus
	for {
		st, err := fetchRestartStatus(port)
		switch {
		case err != nil:
			last = err
		case st.InstanceId == "" || st.InstanceId == notInstanceId:
			// The process we are replacing, or one that will not say who
			// it is. Neither is evidence of a restart.
		case !serverIsVersion(st, wantVersion):
			lastWrong = st
		default:
			return st, nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	switch {
	case lastWrong != nil:
		return nil, &wrongVersionError{Got: upgrade.VersionFromBanner(lastWrong.Version), Want: wantVersion}
	case last != nil:
		return nil, fmt.Errorf("no replacement server answered on port %d within %s (last error: %v)", port, budget, last)
	}
	return nil, fmt.Errorf("no replacement server answered on port %d within %s", port, budget)
}

// wrongVersionError is "something is serving this port, it is not the
// process we replaced, and it is on the wrong version" -- an outcome its
// own type because the caller says something different about it than about
// a port nothing answered on at all.
type wrongVersionError struct {
	Got  string
	Want string
}

func (e *wrongVersionError) Error() string {
	return fmt.Sprintf("a server came back but reports %s, not %s", e.Got, e.Want)
}

// serverIsVersion reports whether st's banner is wantVersion, treating
// "neither side named a version" as not-a-contradiction.
func serverIsVersion(st *restartStatus, wantVersion string) bool {
	got := upgrade.VersionFromBanner(st.Version)
	if got == "" || wantVersion == "" {
		return true
	}
	return upgrade.SameVersion(got, wantVersion)
}

// waitForServerGone polls until nothing answers on the port.
func waitForServerGone(port int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !serverAnswers(port) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// portStaysQuiet reports whether nothing answers on the port for the whole
// of the window -- "gone", as opposed to "not answering at this instant".
func portStaysQuiet(port int, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if serverAnswers(port) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return !serverAnswers(port)
}

// startServerDetached starts a blanket server from binary, in its own
// process group, logging to the upgrade state directory.
//
// Called only when the server will not come back on its own: it exited for
// a supervisor that the exec-mode heuristic got wrong, or this is windows,
// which never self-restarts (brief decision row 9) because a detached
// replacement escapes a service's job object and then holds the database
// lock where `sc stop` cannot reach it.
func startServerDetached(binary string) (int, string, error) {
	logPath := filepath.Join(upgradeStateDir(), "server.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, "", err
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer lf.Close()

	args := []string{}
	if cfg := viper.ConfigFileUsed(); cfg != "" {
		args = append(args, "--config", cfg)
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = filepath.Dir(binary)
	if cfg := viper.ConfigFileUsed(); cfg != "" {
		// Run from the config's directory: blanket's own config resolves
		// relative `database` / `tasks.typesPaths` against the working
		// directory, and starting the replacement somewhere else would
		// quietly point it at a different install.
		cmd.Dir = filepath.Dir(cfg)
	}
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSpawnAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return 0, logPath, err
	}
	// Nothing waits on this process: it is meant to outlive the CLI. On
	// unix that leaves a zombie only until the CLI exits and init reaps
	// it, which is immediately.
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, logPath, nil
}
