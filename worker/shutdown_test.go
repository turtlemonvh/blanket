//go:build !windows

package worker_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/testutil"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// longTaskTypeToml runs far longer than any of the shutdown tests wait, so
// a task that is not killed is caught. It records its pid first, and execs
// so the pid is the process the worker kills.
const longTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 120
command = "echo $$ > task.pid; exec sleep 60"
executor = "bash"
`

// shutdownExitBound is how quickly a signalled worker must exit in these
// tests. It is under worker.ShutdownExitDeadline (10s) on purpose: exiting
// inside it shows the normal path finished, not the backstop timer.
const shutdownExitBound = 8 * time.Second

// TestRun_SIGTERM sends SIGTERM to a real worker subprocess (see TestMain)
// while it runs a long task, with the server in three conditions. In every
// one the worker must kill the task and exit within shutdownExitBound:
//
//   - up: the task ends STOPPED, the worker record is stopped with
//     StopReasonSelf, and the worker exits 0.
//   - down: nothing to report to; the worker exits anyway and leaves the
//     outcome journal for the reaper.
//   - hung: every request is accepted and never answered, which is what a
//     server mid-restart behind a stuck proxy, or a wedged one, looks
//     like. Calls in flight must not hold the shutdown up.
func TestRun_SIGTERM(t *testing.T) {
	for _, mode := range []string{"up", "down", "hung"} {
		t.Run(mode, func(t *testing.T) { runSIGTERMScenario(t, mode) })
	}
}

func runSIGTERMScenario(t *testing.T, mode string) {
	workDir, err := os.MkdirTemp("", "blanket-sigterm-*")
	require.NoError(t, err)
	defer os.RemoveAll(workDir)

	typesDir := filepath.Join(workDir, "types")
	resultsDir := filepath.Join(workDir, "results")
	for _, d := range []string{typesDir, resultsDir} {
		require.NoError(t, os.MkdirAll(d, 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(typesDir, "long_task.toml"), []byte(longTaskTypeToml), 0644))

	sc, dbCleanup := testutil.NewTestServer(t)
	defer dbCleanup()
	sc.ResultsPath = resultsDir
	sc.TimeMultiplier = 1.0

	// hang makes every request block until release is closed. release is
	// closed before the server is, because httptest's Close waits for
	// in-flight handlers.
	var hang atomic.Bool
	release := make(chan struct{})
	router := sc.GetRouter()
	httpSrv, srvCleanup := testutil.NewTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hang.Load() {
			<-release
			return
		}
		router.ServeHTTP(w, r)
	}))
	srvClosed := false
	defer func() {
		if !srvClosed {
			srvCleanup()
		}
	}()
	defer close(release)

	u, err := url.Parse(httpSrv.URL)
	require.NoError(t, err)

	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)
	defer func() {
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}()

	workerID := objectid.NewObjectId()

	resp, err := http.Post(httpSrv.URL+"/task/", "application/json", bytes.NewReader([]byte(`{"type": "long_task"}`)))
	require.NoError(t, err)
	var task tasks.Task
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		subprocessEnvVar+"=1",
		"BLANKET_WORKER_PORT="+u.Port(),
		"BLANKET_WORKER_TYPES_DIR="+typesDir,
		"BLANKET_WORKER_RESULTS_DIR="+resultsDir,
		"BLANKET_WORKER_ID="+workerID.Hex(),
		"BLANKET_WORKER_TAGS=exec:bash,os:unix",
		"BLANKET_WORKER_CHECK_INTERVAL=0.5",
		"BLANKET_WORKER_LOGFILE="+filepath.Join(workDir, "worker.log"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	defer cmd.Process.Kill()

	taskResultDir := filepath.Join(resultsDir, task.Id.Hex())
	require.Eventually(t, func() bool {
		got, err := sc.DB.GetTask(task.Id)
		return err == nil && got.State == "RUNNING"
	}, 10*time.Second, 100*time.Millisecond, "worker never started the task; stderr=%s", &stderr)

	var taskPid int
	require.Eventually(t, func() bool {
		bts, err := os.ReadFile(filepath.Join(taskResultDir, "task.pid"))
		if err != nil {
			return false
		}
		taskPid, err = strconv.Atoi(strings.TrimSpace(string(bts)))
		return err == nil && taskPid > 0
	}, 5*time.Second, 50*time.Millisecond, "task never wrote its pid")

	switch mode {
	case "down":
		srvCleanup()
		srvClosed = true
	case "hung":
		hang.Store(true)
	}

	start := time.Now()
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	var exitErr error
	select {
	case exitErr = <-waitErr:
	case <-time.After(shutdownExitBound):
		t.Fatalf("worker did not exit within %s of SIGTERM (server %s); stdout=%s stderr=%s",
			shutdownExitBound, mode, &stdout, &stderr)
	}
	t.Logf("server %s: worker exited %v after SIGTERM (err=%v)", mode, time.Since(start), exitErr)

	// The task process is gone, whatever happened to the server. It was
	// re-parented when the worker exited, so give init a moment to reap.
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(taskPid, 0), syscall.ESRCH)
	}, 5*time.Second, 50*time.Millisecond, "task process %d outlived the worker", taskPid)

	switch mode {
	case "up":
		assert.NoError(t, exitErr, "a reported shutdown exits 0; stderr=%s", &stderr)

		got, err := sc.DB.GetTask(task.Id)
		require.NoError(t, err)
		assert.Equal(t, "STOPPED", got.State)
		assert.Nil(t, got.ExitCode, "a killed task has no exit code")

		w, err := sc.DB.GetWorker(workerID)
		require.NoError(t, err)
		assert.True(t, w.Stopped)
		assert.Equal(t, worker.StopReasonSelf, w.StoppedReason)

		_, jerr := os.Stat(filepath.Join(taskResultDir, worker.OutcomeJournalFilename))
		assert.True(t, os.IsNotExist(jerr), "a reported outcome removes the journal")
	default:
		// Nothing could be reported, so the outcome journal stays for the
		// reaper, saying the task exited.
		j, err := worker.ReadOutcomeJournal(taskResultDir)
		require.NoError(t, err, "the unreported outcome must stay on disk")
		assert.Equal(t, worker.OutcomeStateExited, j.State)
		assert.Equal(t, -1, j.ExitCode, fmt.Sprintf("journal: %+v", j))
	}
}
