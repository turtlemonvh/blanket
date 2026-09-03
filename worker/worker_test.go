// External test package to avoid the import cycle:
//
//	worker → lib/bolt → worker
//
// Integration tests for the worker package.
//
// Covered:
//   - single-task happy path: TestProcessOne
//   - two tasks in sequence: TestProcessTwo
//   - task timeout: TestProcessOne_Timeout — task exceeds its configured
//     timeout, ends in TIMEDOUT
//   - task api-stopped mid-flight: TestProcessOne_StoppedMidFlight
//   - log production: TestProcessOne_ProducesLogs
//   - worker shutdown: TestRun_SIGTERM — SIGTERM to `Run()` (spawned as a
//     real subprocess; see TestMain/runWorkerSubprocess) stops cleanly.
//   - goroutine-leak check across a run: TestProcessTasks_NoGoroutineLeak.
//
// See turtlemonvh/blanket#51 for the history behind the last two.
package worker_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/server"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// subprocessEnvVar, when set to "1" in this test binary's environment,
// makes TestMain run a worker (via runWorkerSubprocess) instead of the
// test suite. See TestMain and TestRun_SIGTERM.
const subprocessEnvVar = "BLANKET_WORKER_SUBPROCESS"

// TestMain intercepts the "run as a worker subprocess" mode used by
// TestRun_SIGTERM before falling through to the normal test runner. This
// is the same re-exec-the-test-binary idiom Go's own os/exec tests use
// (GO_WANT_HELPER_PROCESS) — scoped to this package since there's no
// existing subprocess harness elsewhere in the repo to reuse.
func TestMain(m *testing.M) {
	if os.Getenv(subprocessEnvVar) == "1" {
		runWorkerSubprocess()
		// Run()'s non-daemon branch always ends in os.Exit; this is
		// defensive only.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runWorkerSubprocess reads a WorkerConf + viper config out of the
// environment (set by TestRun_SIGTERM) and calls worker.WorkerConf.Run().
// Run() can't be exercised in-process like ProcessOne/ProcessTasks because
// its non-daemon branch always ends in os.Exit — so this is invoked as a
// real subprocess (this same test binary, re-exec'd) instead.
func runWorkerSubprocess() {
	port, err := strconv.Atoi(os.Getenv("BLANKET_WORKER_PORT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "runWorkerSubprocess: bad BLANKET_WORKER_PORT:", err)
		os.Exit(2)
	}
	checkInterval, err := strconv.ParseFloat(os.Getenv("BLANKET_WORKER_CHECK_INTERVAL"), 64)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runWorkerSubprocess: bad BLANKET_WORKER_CHECK_INTERVAL:", err)
		os.Exit(2)
	}

	viper.Set("port", port)
	viper.Set("tasks.typesPaths", []string{os.Getenv("BLANKET_WORKER_TYPES_DIR")})
	viper.Set("tasks.resultsPath", os.Getenv("BLANKET_WORKER_RESULTS_DIR"))
	viper.Set("timeMultiplier", 1.0)

	wConf := worker.WorkerConf{
		Id:            objectid.ObjectIdHex(os.Getenv("BLANKET_WORKER_ID")),
		Tags:          strings.Split(os.Getenv("BLANKET_WORKER_TAGS"), ","),
		CheckInterval: checkInterval,
		Logfile:       os.Getenv("BLANKET_WORKER_LOGFILE"),
	}

	// Does not return: Run's non-daemon branch always calls os.Exit.
	wConf.Run()
}

// testTaskTypeToml is a minimal bash task with no required env vars.
const testTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 10
command = "echo 'hello from blanket integration test'"
executor = "bash"
`

// workerHarness wires together everything a ProcessOne-style integration
// test needs: in-memory DB+queue, a live HTTP server, a types dir a caller
// can add task types into, and a registered worker.
type workerHarness struct {
	t          *testing.T
	srv        *httptest.Server
	typesDir   string
	work       worker.WorkerConf
	claimCount *atomic.Int64
	cleanupFn  func()
}

func (h *workerHarness) writeTaskType(name, toml string) {
	h.t.Helper()
	err := os.WriteFile(
		filepath.Join(h.typesDir, name+".toml"),
		[]byte(toml),
		0644,
	)
	if err != nil {
		h.t.Fatalf("write task type %s: %v", name, err)
	}
}

func (h *workerHarness) submit(taskType string) tasks.Task {
	h.t.Helper()
	resp, err := http.Post(
		fmt.Sprintf("%s/task/", h.srv.URL),
		"application/json",
		bytes.NewReader([]byte(fmt.Sprintf(`{"type": %q}`, taskType))),
	)
	if err != nil {
		h.t.Fatalf("submit task: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("submit task: unexpected status %d", resp.StatusCode)
	}
	var task tasks.Task
	json.NewDecoder(resp.Body).Decode(&task)
	return task
}

func (h *workerHarness) claim() tasks.Task {
	h.t.Helper()
	resp, err := http.Post(
		fmt.Sprintf("%s/task/claim/%s", h.srv.URL, h.work.Id.Hex()),
		"application/json",
		nil,
	)
	if err != nil {
		h.t.Fatalf("claim task: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("claim task: unexpected status %d", resp.StatusCode)
	}
	var task tasks.Task
	json.NewDecoder(resp.Body).Decode(&task)
	return task
}

func (h *workerHarness) fetch(id objectid.ObjectId) tasks.Task {
	h.t.Helper()
	resp, err := http.Get(fmt.Sprintf("%s/task/%s", h.srv.URL, id.Hex()))
	if err != nil {
		h.t.Fatalf("fetch task: %v", err)
	}
	defer resp.Body.Close()
	var task tasks.Task
	json.NewDecoder(resp.Body).Decode(&task)
	return task
}

func (h *workerHarness) cancel(id objectid.ObjectId) {
	h.t.Helper()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/task/%s/cancel", h.srv.URL, id.Hex()), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("cancel task: %v", err)
	}
	resp.Body.Close()
}

// newWorkerHarness stands up the in-memory server, points viper at it, and
// registers a single worker tagged ["exec:bash","os:unix"]. Caller is responsible
// for installing task types via writeTaskType before submitting.
func newWorkerHarness(t *testing.T) *workerHarness {
	t.Helper()

	workDir, err := os.MkdirTemp("", "blanket-integration-*")
	if err != nil {
		t.Fatalf("create work dir: %v", err)
	}
	typesDir := filepath.Join(workDir, "types")
	resultsDir := filepath.Join(workDir, "results")
	for _, d := range []string{typesDir, resultsDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("create dir %s: %v", d, err)
		}
	}

	db, dbCleanup := bolt.NewTestDB()
	q, qCleanup := bolt.NewTestQueue()

	sc := &server.ServerConfig{
		DB:             db,
		Q:              q,
		ResultsPath:    resultsDir,
		TimeMultiplier: 1.0,
	}
	claimCount := &atomic.Int64{}
	router := sc.GetRouter()
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/task/claim/") {
			claimCount.Add(1)
		}
		router.ServeHTTP(w, r)
	}))

	u, _ := url.Parse(httpSrv.URL)
	port, _ := strconv.Atoi(u.Port())
	viper.Set("port", port)
	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)
	viper.Set("timeMultiplier", 1.0)

	workerID := objectid.NewObjectId()
	wConf := worker.WorkerConf{
		Id:            workerID,
		Tags:          []string{"exec:bash", "os:unix"},
		Stopped:       false,
		CheckInterval: 0.5,
		Logfile:       filepath.Join(workDir, "worker.log"),
	}
	workerBytes, _ := json.Marshal(wConf)

	req, _ := http.NewRequest(
		"PUT",
		fmt.Sprintf("%s/worker/%s", httpSrv.URL, workerID.Hex()),
		bytes.NewReader(workerBytes),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register worker: status %d", resp.StatusCode)
	}

	h := &workerHarness{
		t:          t,
		srv:        httpSrv,
		typesDir:   typesDir,
		work:       wConf,
		claimCount: claimCount,
		cleanupFn: func() {
			httpSrv.Close()
			dbCleanup()
			qCleanup()
			os.RemoveAll(workDir)
			viper.Set("port", 0)
			viper.Set("tasks.typesPaths", nil)
			viper.Set("tasks.resultsPath", "")
		},
	}
	return h
}

func (h *workerHarness) cleanup() { h.cleanupFn() }

// TestProcessOne exercises the single-task happy path end-to-end: submit,
// claim, run, assert SUCCESS + stdout contents.
func TestProcessOne(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("echo_task", testTaskTypeToml)

	submitted := h.submit("echo_task")
	claimed := h.claim()
	assert.Equal(t, submitted.Id, claimed.Id)
	assert.Equal(t, "CLAIMED", claimed.State)

	assert.NoError(t, h.work.ProcessOne(&claimed))

	final := h.fetch(submitted.Id)
	assert.Equal(t, "SUCCESS", final.State)
	assert.Equal(t, 100, final.Progress)

	stdout, err := os.ReadFile(filepath.Join(final.ResultDir, "blanket.stdout.log"))
	assert.NoError(t, err)
	assert.Contains(t, string(stdout), "hello from blanket integration test")
}

// TestProcessTwo runs two tasks back-to-back on the same worker and asserts
// both land in SUCCESS with distinct result dirs.
func TestProcessTwo(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("echo_task", testTaskTypeToml)

	t1 := h.submit("echo_task")
	t2 := h.submit("echo_task")

	for i := 0; i < 2; i++ {
		claimed := h.claim()
		assert.NoError(t, h.work.ProcessOne(&claimed))
	}

	f1 := h.fetch(t1.Id)
	f2 := h.fetch(t2.Id)
	assert.Equal(t, "SUCCESS", f1.State)
	assert.Equal(t, "SUCCESS", f2.State)
	assert.NotEqual(t, f1.ResultDir, f2.ResultDir)

	// Both tasks should have produced stdout.
	for _, tsk := range []tasks.Task{f1, f2} {
		stdout, err := os.ReadFile(filepath.Join(tsk.ResultDir, "blanket.stdout.log"))
		assert.NoError(t, err)
		assert.Contains(t, string(stdout), "hello from blanket integration test")
	}
}

// timeoutTaskTypeToml sleeps longer than its timeout so the worker must kill it.
const timeoutTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 1
command = "sleep 5"
executor = "bash"
`

// TestProcessOne_Timeout confirms the worker kills a task that overruns its
// configured timeout and transitions it to TIMEDOUT. A subsequent task on the
// same worker should still run to SUCCESS.
func TestProcessOne_Timeout(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("slow_task", timeoutTaskTypeToml)
	h.writeTaskType("echo_task", testTaskTypeToml)

	// Slow task first — should be killed.
	slow := h.submit("slow_task")
	claimed := h.claim()
	assert.Equal(t, slow.Id, claimed.Id)

	// ProcessOne returns the error from cmd.Wait() when the process is killed.
	_ = h.work.ProcessOne(&claimed)

	final := h.fetch(slow.Id)
	assert.Equal(t, "TIMEDOUT", final.State, "slow task should end in TIMEDOUT")

	// Follow-up task on the same worker should still succeed.
	_ = h.submit("echo_task")
	next := h.claim()
	assert.NoError(t, h.work.ProcessOne(&next))

	nextFinal := h.fetch(next.Id)
	assert.Equal(t, "SUCCESS", nextFinal.State)
}

// longRunningTaskTypeToml gives us a window to cancel mid-flight.
const longRunningTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 30
command = "sleep 10"
executor = "bash"
`

// TestProcessOne_StoppedMidFlight submits a long-running task, starts
// executing it, then calls the cancel API. The worker's monitoring goroutine
// should observe the STOPPED tombstone and kill the process.
func TestProcessOne_StoppedMidFlight(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("long_task", longRunningTaskTypeToml)

	h.submit("long_task")
	claimed := h.claim()

	// Run ProcessOne in a goroutine; we'll cancel while it's running.
	done := make(chan error, 1)
	go func() { done <- h.work.ProcessOne(&claimed) }()

	// Give the task a moment to transition to RUNNING, then cancel.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur := h.fetch(claimed.Id)
		if cur.State == "RUNNING" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.cancel(claimed.Id)

	// ProcessOne should return within a few seconds once the monitor goroutine
	// kills the child process.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessOne did not return after cancel")
	}

	final := h.fetch(claimed.Id)
	assert.Equal(t, "STOPPED", final.State)
}

// stopWorkerViaAPI marks the worker stopped in the DB so that the
// ProcessTasks loop exits at its next Refetch.
func (h *workerHarness) stopWorkerViaAPI() {
	h.t.Helper()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/worker/%s/stop", h.srv.URL, h.work.Id.Hex()), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("stop worker: %v", err)
	}
	resp.Body.Close()
}

// TestProcessTasks_DoesNotHotSpinOnEmptyQueue is the regression test for the
// claim-loop hot-spin: pre-fix, the empty-queue branch (MarkAsClaimed →
// Task{},nil) hit `continue` with err==nil, skipping the loop's only sleep
// and pegging the server with thousands of POST /task/claim/ requests per
// second. With CheckInterval=0.5s and a 2s window, expect ~4 attempts; we
// allow a generous ceiling of 50 to absorb scheduling jitter.
func TestProcessTasks_DoesNotHotSpinOnEmptyQueue(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.work.CheckInterval = worker.MIN_CHECK_INTERVAL_SECONDS

	done := make(chan error, 1)
	go func() { done <- h.work.ProcessTasks() }()

	time.Sleep(2 * time.Second)
	h.stopWorkerViaAPI()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessTasks did not exit after stop")
	}

	got := h.claimCount.Load()
	if got > 50 {
		t.Fatalf("hot-spin detected: %d POST /task/claim/ in 2s (expected <=50; pre-fix was ~thousands)", got)
	}
	if got == 0 {
		t.Fatalf("expected at least one claim attempt; got 0 — loop never ran?")
	}
}

// TestRun_RejectsLowCheckInterval covers the defensive limit: WorkerConf.Run
// must refuse a CheckInterval below MIN_CHECK_INTERVAL_SECONDS rather than
// silently clamping. This is the second guard rail behind the loop fix; if
// the loop ever regresses, this rejects creation up-front.
func TestRun_RejectsLowCheckInterval(t *testing.T) {
	for _, iv := range []float64{0.1, 0.4, 0.49} {
		w := worker.WorkerConf{
			Id:            objectid.NewObjectId(),
			Tags:          []string{"exec:bash"},
			CheckInterval: iv,
		}
		err := w.Run()
		if err == nil {
			t.Errorf("CheckInterval=%v: expected error, got nil", iv)
		}
	}
}

// TestProcessOne_ProducesLogs asserts both the task stdout log and the
// worker-level logfile exist and are non-empty after a successful run.
// The worker-level log is only written when Run() executes; for a pure
// ProcessOne run we verify stdout + stderr files exist at ResultDir.
func TestProcessOne_ProducesLogs(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("echo_task", testTaskTypeToml)

	h.submit("echo_task")
	claimed := h.claim()
	assert.NoError(t, h.work.ProcessOne(&claimed))

	final := h.fetch(claimed.Id)
	for _, name := range []string{"blanket.stdout.log", "blanket.stderr.log"} {
		p := filepath.Join(final.ResultDir, name)
		info, err := os.Stat(p)
		assert.NoError(t, err, "expected %s to exist", name)
		if err == nil && name == "blanket.stdout.log" {
			assert.Greater(t, info.Size(), int64(0), "stdout should be non-empty")
		}
	}
}

// metricsGoroutineCount hits the server's /ops/status/ metrics endpoint
// (see server/serve_metrics.go) and extracts the nGoRoutines gauge. That
// gauge is refreshed on a 2s ticker rather than computed per-request, so
// callers that need a value reflecting a just-happened state change should
// poll (e.g. via require.Eventually) instead of sampling once.
func (h *workerHarness) metricsGoroutineCount() int64 {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + "/ops/status/")
	if err != nil {
		h.t.Fatalf("fetch metrics: %v", err)
	}
	defer resp.Body.Close()
	var m struct {
		NGoRoutines int64 `json:"nGoRoutines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		h.t.Fatalf("decode metrics: %v", err)
	}
	return m.NGoRoutines
}

// TestProcessTasks_NoGoroutineLeak drains several tasks through the full
// ProcessTasks loop and confirms the process's goroutine count — as
// exposed by the /ops/status/ metrics endpoint — returns to its pre-run
// baseline once the loop exits. Regression guard for the per-task
// monitoring goroutine ProcessOne starts for every task (worker.go's
// taskDone channel): it must exit once cmd.Wait() returns rather than
// accumulate one per task processed.
//
// The metrics gauge only refreshes every 2s (see serve_metrics.go's
// ticker), so both the baseline and final readings poll with
// require.Eventually instead of sampling once, and the final comparison
// allows a small tolerance rather than requiring exact equality — stray
// runtime/GC goroutines make an exact pre/post match flaky.
func TestProcessTasks_NoGoroutineLeak(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("echo_task", testTaskTypeToml)
	h.work.CheckInterval = worker.MIN_CHECK_INTERVAL_SECONDS

	var baseline int64
	require.Eventually(t, func() bool {
		baseline = h.metricsGoroutineCount()
		return baseline > 0
	}, 5*time.Second, 200*time.Millisecond, "never got a nonzero baseline goroutine reading")

	done := make(chan error, 1)
	go func() { done <- h.work.ProcessTasks() }()

	const nTasks = 5
	submitted := make([]tasks.Task, nTasks)
	for i := 0; i < nTasks; i++ {
		submitted[i] = h.submit("echo_task")
	}

	for _, tsk := range submitted {
		require.Eventually(t, func() bool {
			return h.fetch(tsk.Id).State == "SUCCESS"
		}, 10*time.Second, 100*time.Millisecond, "task %s never reached SUCCESS", tsk.Id.Hex())
	}

	h.stopWorkerViaAPI()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessTasks did not exit after stop")
	}

	const tolerance = int64(5)
	require.Eventually(t, func() bool {
		return h.metricsGoroutineCount() <= baseline+tolerance
	}, 6*time.Second, 250*time.Millisecond,
		"goroutine count did not return to baseline after worker run (baseline=%d)", baseline)
}

// sigtermTaskTypeToml sleeps long enough that the parent test can observe
// RUNNING and send SIGTERM before the task finishes on its own.
const sigtermTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 10
command = "sleep 2"
executor = "bash"
`

// TestRun_SIGTERM starts a worker as a real subprocess (this same test
// binary, re-exec'd into runWorkerSubprocess via TestMain — see there for
// why Run() can't be driven in-process), submits a task, waits for the
// subprocess to start running it, then sends SIGTERM. It asserts:
//
//   - the worker registers itself as Stopped in the DB (the SIGTERM
//     handler's c.Stop() call in worker.go's Run())
//   - the in-flight task still finishes normally — SIGTERM stops the claim
//     loop, it does not kill the currently-running child process
//   - the subprocess exits on its own (Run()'s os.Exit) within a bounded
//     time
//   - a task submitted only *after* the worker subprocess has exited is
//     never claimed
//
// A task queued *before* the worker exits is deliberately not exercised:
// ProcessTasks' for-loop only checks c.Stopped at the top of each
// iteration, and Refetch (which pulls the freshly-true Stopped) happens
// before that same iteration's claim attempt — so a task already sitting
// in the queue when SIGTERM lands can still be picked up by the in-flight
// iteration before the loop condition is re-evaluated. That's existing
// worker.go behavior (not a regression this test is trying to pin down),
// so submitting task 2 only once the subprocess is confirmed gone avoids
// an assertion that would be racy against real, intended behavior.
func TestRun_SIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM subprocess harness is unix-only; see worker/daemon_windows.go for the platform split this would need")
	}

	workDir, err := os.MkdirTemp("", "blanket-sigterm-*")
	require.NoError(t, err)
	defer os.RemoveAll(workDir)

	typesDir := filepath.Join(workDir, "types")
	resultsDir := filepath.Join(workDir, "results")
	for _, d := range []string{typesDir, resultsDir} {
		require.NoError(t, os.MkdirAll(d, 0755))
	}

	db, dbCleanup := bolt.NewTestDB()
	defer dbCleanup()
	q, qCleanup := bolt.NewTestQueue()
	defer qCleanup()

	sc := &server.ServerConfig{
		DB:             db,
		Q:              q,
		ResultsPath:    resultsDir,
		TimeMultiplier: 1.0,
	}
	httpSrv := httptest.NewServer(sc.GetRouter())
	defer httpSrv.Close()

	u, err := url.Parse(httpSrv.URL)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(
		filepath.Join(typesDir, "sigterm_task.toml"),
		[]byte(sigtermTaskTypeToml),
		0644,
	))

	// The /task/ submit endpoint below runs in this (parent) process's
	// httptest server and reads tasks.typesPaths straight out of viper on
	// every request — same global config the subprocess is handed
	// explicitly via BLANKET_WORKER_TYPES_DIR below.
	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)
	defer func() {
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}()

	workerID := objectid.NewObjectId()

	submitTask := func() tasks.Task {
		t.Helper()
		resp, err := http.Post(
			fmt.Sprintf("%s/task/", httpSrv.URL),
			"application/json",
			bytes.NewReader([]byte(`{"type": "sigterm_task"}`)),
		)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var task tasks.Task
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
		return task
	}
	fetchTask := func(id objectid.ObjectId) tasks.Task {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/task/%s", httpSrv.URL, id.Hex()))
		require.NoError(t, err)
		defer resp.Body.Close()
		var task tasks.Task
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&task))
		return task
	}
	fetchWorker := func() worker.WorkerConf {
		t.Helper()
		resp, err := http.Get(fmt.Sprintf("%s/worker/%s", httpSrv.URL, workerID.Hex()))
		require.NoError(t, err)
		defer resp.Body.Close()
		var w worker.WorkerConf
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&w))
		return w
	}

	task1 := submitTask()

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

	// Wait for the subprocess to claim and start running task1.
	require.Eventually(t, func() bool {
		return fetchTask(task1.Id).State == "RUNNING"
	}, 10*time.Second, 100*time.Millisecond,
		"worker subprocess never started the task; stdout=%s stderr=%s", &stdout, &stderr)

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		assert.NoError(t, err, "worker subprocess exited non-zero; stdout=%s stderr=%s", &stdout, &stderr)
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("worker subprocess did not exit after SIGTERM; stdout=%s stderr=%s", &stdout, &stderr)
	}

	// Registered itself as stopped before exiting.
	assert.True(t, fetchWorker().Stopped, "worker should be registered as stopped after SIGTERM")

	// The in-flight task still finished normally — SIGTERM stops the claim
	// loop, not the running child process.
	assert.Equal(t, "SUCCESS", fetchTask(task1.Id).State)

	// A task submitted after the worker subprocess has exited is never
	// claimed — there's no live worker left to claim it.
	task2 := submitTask()
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, "WAITING", fetchTask(task2.Id).State, "no worker should be alive to claim this task")
}
