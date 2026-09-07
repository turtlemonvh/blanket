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

// waitForNewServer polls until a server answers with an instance id other
// than notInstanceId, or the budget runs out.
//
// The instance id, and not the fact that *something* answered, is what
// makes this a verification. The old server keeps serving right up until
// its listener closes, so a poll that accepted the first 200 it saw would
// routinely "verify" the process it was replacing.
func waitForNewServer(port int, notInstanceId string, budget time.Duration) (*restartStatus, error) {
	deadline := time.Now().Add(budget)
	var last error
	for time.Now().Before(deadline) {
		st, err := fetchRestartStatus(port)
		if err == nil && st.InstanceId != "" && st.InstanceId != notInstanceId {
			return st, nil
		}
		if err != nil {
			last = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	if last != nil {
		return nil, fmt.Errorf("no replacement server answered on port %d within %s (last error: %v)", port, budget, last)
	}
	return nil, fmt.Errorf("no replacement server answered on port %d within %s", port, budget)
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
