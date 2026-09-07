// External test package to avoid the import cycle:
//
//	worker → lib/testutil → lib/bolt → worker
//	worker → lib/testutil → server → worker
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
//   - exit code plumbing (cmd.Wait -> MarkAsFinished -> stored task):
//     TestProcessOne_ExitCode, TestProcessOne_TimeoutHasNoExitCode
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
	"github.com/turtlemonvh/blanket/lib/combined_log"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/testutil"
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

	// Lets a parent test shorten how long the SIGTERM handler will keep
	// trying to reach a server that is never coming back; see
	// TestRun_SIGTERM_ServerUnreachable.
	if ms := os.Getenv("BLANKET_WORKER_SHUTDOWN_DEADLINE_MS"); ms != "" {
		d, err := strconv.Atoi(ms)
		if err != nil {
			fmt.Fprintln(os.Stderr, "runWorkerSubprocess: bad BLANKET_WORKER_SHUTDOWN_DEADLINE_MS:", err)
			os.Exit(2)
		}
		worker.ShutdownRetryDeadline = time.Duration(d) * time.Millisecond
	}

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
	resultsDir string
	// work is handed to WorkerConf.ProcessTasks, which overwrites the
	// whole struct on every Refetch. Anything the test goroutine needs
	// while the loop is running must come from workerID instead, not from
	// work.Id -- reading it there is a genuine data race.
	work       worker.WorkerConf
	workerID   objectid.ObjectId
	claimCount *atomic.Int64
	// requestCount counts every request that reaches the harness, broken
	// ones included — the signal the backoff tests measure.
	requestCount *atomic.Int64
	outage       *outageInjector
	cleanupFn    func()
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
		fmt.Sprintf("%s/task/claim/%s", h.srv.URL, h.workerID.Hex()),
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

// cancel calls PUT /task/:id/cancel with ?force=true, the parameter required
// to stop a RUNNING task (see turtlemonvh/blanket#52). A WAITING task also
// accepts the param harmlessly, so tests can always use this helper.
func (h *workerHarness) cancel(id objectid.ObjectId) {
	h.t.Helper()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/task/%s/cancel?force=true", h.srv.URL, id.Hex()), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("cancel task: %v", err)
	}
	defer resp.Body.Close()
	// Asserted, not ignored: the server refuses to cancel a task that is
	// neither queued nor RUNNING — a CLAIMED one, say, see
	// server.cancelTaskById — and a caller that drops that answer on the
	// floor goes on to assert against a task that was never cancelled. That
	// is exactly how turtlemonvh/blanket#116 stayed hidden.
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("cancel task %s: unexpected status %d", id.Hex(), resp.StatusCode)
	}
}

// waitForState blocks until the server reports the task in the given
// state. A test that acts on a task mid-run (cancelling it, say) needs the
// server's view to have caught up first: the worker reaches a state
// locally before the round trip that records it lands.
func (h *workerHarness) waitForState(id objectid.ObjectId, state string, within time.Duration) {
	h.t.Helper()
	require.Eventually(h.t, func() bool {
		return h.fetch(id).State == state
	}, within, 50*time.Millisecond, "task %s never reached %s", id.Hex(), state)
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

	sc, dbCleanup := testutil.NewTestServer(t)
	sc.ResultsPath = resultsDir
	sc.TimeMultiplier = 1.0

	claimCount := &atomic.Int64{}
	requestCount := &atomic.Int64{}
	outage := &outageInjector{}
	router := sc.GetRouter()
	httpSrv, srvCleanup := testutil.NewTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/task/claim/") {
			claimCount.Add(1)
		}
		if outage.intercept(w, r) {
			return
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
		t:            t,
		srv:          httpSrv,
		typesDir:     typesDir,
		resultsDir:   resultsDir,
		work:         wConf,
		workerID:     workerID,
		claimCount:   claimCount,
		requestCount: requestCount,
		outage:       outage,
		cleanupFn: func() {
			srvCleanup()
			dbCleanup()
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
	// ProcessOne refreshes the task it is handed, so this goroutine owns
	// `claimed` from here on; the test reads the id it captured first.
	taskId := claimed.Id

	// Run ProcessOne in a goroutine; we'll cancel while it's running.
	done := make(chan error, 1)
	go func() { done <- h.work.ProcessOne(&claimed) }()

	// RUNNING first: a cancel that arrives while the task is still CLAIMED
	// is refused, and the run would then finish normally.
	h.waitForState(taskId, "RUNNING", 10*time.Second)
	h.cancel(taskId)

	// ProcessOne should return within a few seconds once the monitor goroutine
	// kills the child process.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessOne did not return after cancel")
	}

	final := h.fetch(taskId)
	assert.Equal(t, "STOPPED", final.State)
}

// stopWorkerViaAPI marks the worker stopped in the DB so that the
// ProcessTasks loop exits at its next Refetch.
func (h *workerHarness) stopWorkerViaAPI() {
	h.t.Helper()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/worker/%s/stop", h.srv.URL, h.workerID.Hex()), nil)
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

// interleavedTaskTypeToml alternates between the two streams, so a
// recording that merely concatenated the files would come out in a
// different order than the task produced.
//
// The sleeps are load-bearing. What the worker records is the order its
// two tailers saw the lines land, which is the same basis the live log
// view shows lines on -- but a task that writes everything at once and
// exits leaves both files to be read in whatever order the two tailers
// are scheduled in. Spacing the writes out makes the observed order the
// task's own order, which is what this test is about.
const interleavedTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 10
command = "echo out-one; sleep 0.2; echo err-one 1>&2; sleep 0.2; echo out-two; sleep 0.2; echo err-two 1>&2; sleep 0.2; printf 'no-newline'"
executor = "bash"
`

// readCombinedLog parses a finished task's combined record.
func readCombinedLog(t *testing.T, resultDir string) []combined_log.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(resultDir, combined_log.FileName))
	require.NoError(t, err, "combined log should exist at %s", resultDir)
	var recs []combined_log.Record
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		rec, ok := combined_log.ParseRecord(line)
		require.True(t, ok, "unparseable combined log record %q", line)
		recs = append(recs, rec)
	}
	return recs
}

// The review finding behind turtlemonvh/blanket#104's second pass: the
// order a task's two streams were produced in exists nowhere on disk if
// the child writes straight into the two files. The worker now copies
// both through itself and records each completed line, tagged and in
// arrival order -- while leaving the two per-stream files exactly as they
// were, since every other reader of a task's output still reads those.
func TestProcessOne_RecordsCombinedLog(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("interleaved", interleavedTaskTypeToml)

	h.submit("interleaved")
	claimed := h.claim()
	require.NoError(t, h.work.ProcessOne(&claimed))

	final := h.fetch(claimed.Id)
	require.Equal(t, "SUCCESS", final.State)

	recs := readCombinedLog(t, final.ResultDir)
	var got []string
	for _, r := range recs {
		got = append(got, r.Stream+":"+r.Line)
	}
	// The trailing printf never emits a newline; it is flushed when the
	// recorder closes rather than dropped.
	want := []string{
		"stdout:out-one",
		"stderr:err-one",
		"stdout:out-two",
		"stderr:err-two",
		"stdout:no-newline",
	}
	if runtime.GOOS == "windows" {
		// Windows tails by polling (see tail_watch_windows.go), so two
		// lines written within one poll interval of each other on
		// different streams come out grouped by file -- the documented
		// limitation. The record is still complete, still per-stream
		// ordered, and still ends with the flushed partial line.
		assert.ElementsMatch(t, want, got, "every record is present exactly once")
		assert.Equal(t, "stdout:no-newline", got[len(got)-1], "the unterminated final line is flushed last")
		perStream := map[string]int{}
		for _, r := range recs {
			perStream[r.Stream]++
			assert.Equal(t, perStream[r.Stream], r.Seq, "seq counts within a stream, from 1, in file order")
		}
	} else {
		assert.Equal(t, want, got, "records should be in the order the task produced them")
		// seq counts within a stream, from 1.
		assert.Equal(t, []int{1, 1, 2, 2, 3},
			[]int{recs[0].Seq, recs[1].Seq, recs[2].Seq, recs[3].Seq, recs[4].Seq})
	}

	for _, r := range recs {
		assert.Greater(t, r.Ts, int64(0), "every record is timestamped")
	}

	// The per-stream files are untouched by any of this: same bytes the
	// task wrote, in the same shape as before the recorder existed.
	stdout, err := os.ReadFile(filepath.Join(final.ResultDir, "blanket.stdout.log"))
	require.NoError(t, err)
	assert.Equal(t, "out-one\nout-two\nno-newline", string(stdout))
	stderr, err := os.ReadFile(filepath.Join(final.ResultDir, "blanket.stderr.log"))
	require.NoError(t, err)
	assert.Equal(t, "err-one\nerr-two\n", string(stderr))
}

// The knob off restores the pre-#104 execution shape exactly: no combined
// record, no pipe, the child writing straight into the two files.
func TestProcessOne_CombinedLogDisabled(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	viper.Set("workers.combinedLog", false)
	defer viper.Set("workers.combinedLog", nil)

	h.writeTaskType("interleaved", interleavedTaskTypeToml)

	h.submit("interleaved")
	claimed := h.claim()
	require.NoError(t, h.work.ProcessOne(&claimed))

	final := h.fetch(claimed.Id)
	assert.Equal(t, "SUCCESS", final.State)

	_, err := os.Stat(filepath.Join(final.ResultDir, combined_log.FileName))
	assert.True(t, os.IsNotExist(err), "no combined log should be written when the knob is off")

	stdout, err := os.ReadFile(filepath.Join(final.ResultDir, "blanket.stdout.log"))
	require.NoError(t, err)
	assert.Equal(t, "out-one\nout-two\nno-newline", string(stdout))
}

// orphanTaskTypeToml backgrounds a process that goes on writing after
// the task itself has exited. Its stdout is the task's stdout, which is
// blanket.stdout.log itself -- not a pipe the worker could close under
// it.
const orphanTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 60
command = "echo before; (sleep 2; echo late) & echo after"
executor = "bash"
`

// The reason the combined log is built by tailing the two files rather
// than by copying the child's output through the worker
// (turtlemonvh/blanket#104 review): a task that leaves something running
// behind it must finish as soon as *it* exits, and the thing it left
// behind must keep writing into the task's log file. A pipe can do
// neither -- cmd.Wait() blocks on it for the orphan's whole life, and
// bounding that with WaitDelay closes the orphan's stdout under it, so
// its output is lost and it may die of SIGPIPE.
//
// The cost, asserted here too, is that the combined record stops at the
// grace window: the orphan's late output is in blanket.stdout.log, where
// the per-stream views and the raw result routes show it, but not in the
// `both` view's ordering record.
func TestProcessOne_OrphanKeepsWritingAfterTaskFinishes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash task type")
	}
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("orphan", orphanTaskTypeToml)

	h.submit("orphan")
	claimed := h.claim()

	start := time.Now()
	require.NoError(t, h.work.ProcessOne(&claimed))
	elapsed := time.Since(start)

	// The orphan writes at ~2s; the drain after the child exits is a
	// 400ms quiet window. Finishing well inside 2s is what proves the
	// worker never waited on the orphan.
	assert.Less(t, elapsed, 1500*time.Millisecond,
		"the task must finish when it exits, not when the process it orphaned does")

	final := h.fetch(claimed.Id)
	assert.Equal(t, "SUCCESS", final.State, "the task itself exited 0")
	require.NotNil(t, final.ExitCode)
	assert.Equal(t, 0, *final.ExitCode)

	stdoutPath := filepath.Join(final.ResultDir, "blanket.stdout.log")
	stdout, err := os.ReadFile(stdoutPath)
	require.NoError(t, err)
	assert.Contains(t, string(stdout), "before")
	assert.Contains(t, string(stdout), "after")
	assert.NotContains(t, string(stdout), "late", "the orphan has not written yet")

	// The combined record is closed before the task is reported finished,
	// so it is complete as of that moment -- and stops there.
	var lines []string
	for _, r := range readCombinedLog(t, final.ResultDir) {
		lines = append(lines, r.Line)
	}
	assert.Equal(t, []string{"before", "after"}, lines)

	// And now the point: the orphan is still writing into the task's
	// stdout file, long after the task is FINISHED. Poll rather than
	// sleep the full duration.
	deadline := time.Now().Add(10 * time.Second)
	var late string
	for time.Now().Before(deadline) {
		b, rerr := os.ReadFile(stdoutPath)
		if rerr == nil && strings.Contains(string(b), "late") {
			late = string(b)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Contains(t, late, "late",
		"a process the task left behind must go on appending to blanket.stdout.log")
}

// graceTaskTypeToml exits immediately but leaves a child that writes one
// line a fraction of a second later -- inside the drain's quiet window.
const graceTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 60
command = "echo first; (sleep 0.1; echo inside-grace) &"
executor = "bash"
`

// The last lines of a task routinely land on disk just after the process
// itself is gone. The worker therefore keeps tailing until both files
// have been quiet for combinedTailGrace (400ms, timeMultiplier-scaled)
// before it closes the combined record -- and only then reports the task
// finished, so what the `both` pane shows for a FINISHED task is what it
// will always show.
func TestProcessOne_CombinedLogCoversGraceWindow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash task type")
	}
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("grace", graceTaskTypeToml)

	h.submit("grace")
	claimed := h.claim()
	require.NoError(t, h.work.ProcessOne(&claimed))

	final := h.fetch(claimed.Id)
	require.Equal(t, "SUCCESS", final.State)

	var lines []string
	for _, r := range readCombinedLog(t, final.ResultDir) {
		lines = append(lines, r.Line)
	}
	assert.Equal(t, []string{"first", "inside-grace"}, lines,
		"a line written within the grace window after exit belongs in the combined log")
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
	for _, name := range []string{"blanket.stdout.log", "blanket.stderr.log", combined_log.FileName} {
		p := filepath.Join(final.ResultDir, name)
		info, err := os.Stat(p)
		assert.NoError(t, err, "expected %s to exist", name)
		if err == nil && name == "blanket.stdout.log" {
			assert.Greater(t, info.Size(), int64(0), "stdout should be non-empty")
		}
	}
}

// closeIdleHTTPConns drops every keep-alive connection cached by this
// test's own client and by the worker's (lib/httpx's shared client), so a
// goroutine count taken afterwards measures goroutines rather than
// connection pools.
//
// An idle keep-alive connection is not free in goroutine terms: net/http
// keeps a readLoop and a writeLoop per pooled client connection, plus a
// conn.serve on the server side, and lib/httpx caches up to 8 per host for
// a full minute (MaxIdleConnsPerHost/IdleConnTimeout). That is cached
// capacity, not a leak — but runtime.NumGoroutine() counts it like one,
// and how much of it exists after a run depends on how many requests
// happened to overlap, i.e. on how slow the machine is. See
// TestProcessTasks_NoGoroutineLeak.
func closeIdleHTTPConns() {
	http.DefaultClient.CloseIdleConnections()
	httpx.Client().CloseIdleConnections()
}

// goroutineDump renders every goroutine's stack, for a failure message
// that says which goroutines are still around rather than only how many.
func goroutineDump() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}

// TestProcessTasks_NoGoroutineLeak drains several tasks through the full
// ProcessTasks loop and confirms the process's goroutine count returns to
// its pre-run baseline once the loop exits. Regression guard for the
// per-task monitoring goroutine ProcessOne starts for every task
// (worker.go's taskDone channel): it must exit once cmd.Wait() returns
// rather than accumulate one per task processed.
//
// The worker under test runs in this process, so runtime.NumGoroutine() is
// the reading. It used to come from the server's /ops/status/ nGoRoutines
// gauge instead, which reports the same number but only as of its last 2s
// tick — and only at the cost of an HTTP round trip that opened a
// connection of its own, perturbing what was being measured. (That
// endpoint is covered by server/serve_tasks_test.go and the Playwright
// suite.)
//
// Both readings are taken with the HTTP connection pools emptied, because
// a pooled connection costs goroutines that no amount of waiting reclaims
// within a test's lifetime — see closeIdleHTTPConns. Without that, this
// measured peak request concurrency as much as it measured leaks, and so
// failed on the (slower, and therefore more overlapping) Windows CI runner
// while passing everywhere else: turtlemonvh/blanket#116.
//
// A small tolerance survives: stray runtime/GC goroutines make an exact
// pre/post match flaky.
func TestProcessTasks_NoGoroutineLeak(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("echo_task", testTaskTypeToml)
	h.work.CheckInterval = worker.MIN_CHECK_INTERVAL_SECONDS

	closeIdleHTTPConns()
	baseline := runtime.NumGoroutine()

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

	// A server-side connection goroutine exits a moment after its peer
	// hangs up, so this polls rather than sampling once.
	const tolerance = 5
	deadline := time.Now().Add(10 * time.Second)
	for {
		closeIdleHTTPConns()
		after := runtime.NumGoroutine()
		if after <= baseline+tolerance {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine count did not return to baseline after worker run "+
				"(baseline=%d, now=%d, tolerance=%d)\n%s",
				baseline, after, tolerance, goroutineDump())
		}
		time.Sleep(250 * time.Millisecond)
	}
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

	sc, dbCleanup := testutil.NewTestServer(t)
	defer dbCleanup()
	sc.ResultsPath = resultsDir
	sc.TimeMultiplier = 1.0

	httpSrv, srvCleanup := testutil.NewTestHTTPServer(t, sc.GetRouter())
	defer srvCleanup()

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

// TestRun_SIGTERM_ServerUnreachable is the regression test for the
// shutdown-path spin (turtlemonvh/blanket#23 phase 1).
//
// The old handler's retry loop never reassigned err and never incremented
// its counter, so with the server gone it looped forever; and even if it
// had given up, the claim loop's only exit condition was the Stopped flag,
// which the worker can only learn about *from the server*. Between the two,
// SIGTERM did nothing at all when the server was down — precisely the
// situation an upgrade creates.
//
// Here the server is torn down first, then SIGTERM is delivered, and the
// worker must still exit within a bounded time. A nonzero exit status is
// expected and fine: the claim loop really did end on an error.
func TestRun_SIGTERM_ServerUnreachable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM subprocess harness is unix-only; see worker/daemon_windows.go for the platform split this would need")
	}

	workDir, err := os.MkdirTemp("", "blanket-sigterm-down-*")
	require.NoError(t, err)
	defer os.RemoveAll(workDir)

	typesDir := filepath.Join(workDir, "types")
	resultsDir := filepath.Join(workDir, "results")
	for _, d := range []string{typesDir, resultsDir} {
		require.NoError(t, os.MkdirAll(d, 0755))
	}

	sc, dbCleanup := testutil.NewTestServer(t)
	defer dbCleanup()
	sc.ResultsPath = resultsDir
	sc.TimeMultiplier = 1.0

	httpSrv, srvCleanup := testutil.NewTestHTTPServer(t, sc.GetRouter())
	srvClosed := false
	defer func() {
		if !srvClosed {
			srvCleanup()
		}
	}()

	u, err := url.Parse(httpSrv.URL)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(
		filepath.Join(typesDir, "sigterm_task.toml"),
		[]byte(sigtermTaskTypeToml),
		0644,
	))
	viper.Set("tasks.typesPaths", []string{typesDir})
	viper.Set("tasks.resultsPath", resultsDir)
	defer func() {
		viper.Set("tasks.typesPaths", nil)
		viper.Set("tasks.resultsPath", "")
	}()

	workerID := objectid.NewObjectId()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		subprocessEnvVar+"=1",
		"BLANKET_WORKER_PORT="+u.Port(),
		"BLANKET_WORKER_TYPES_DIR="+typesDir,
		"BLANKET_WORKER_RESULTS_DIR="+resultsDir,
		"BLANKET_WORKER_ID="+workerID.Hex(),
		"BLANKET_WORKER_TAGS=exec:bash,os:unix",
		"BLANKET_WORKER_CHECK_INTERVAL=0.5",
		"BLANKET_WORKER_SHUTDOWN_DEADLINE_MS=1000",
		"BLANKET_WORKER_LOGFILE="+filepath.Join(workDir, "worker.log"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())

	// Wait until the worker has registered itself, so we know its claim
	// loop is actually running before we pull the server out from under it.
	require.Eventually(t, func() bool {
		w, err := sc.DB.GetWorker(workerID)
		return err == nil && w.Pid != 0
	}, 10*time.Second, 100*time.Millisecond,
		"worker subprocess never registered; stdout=%s stderr=%s", &stdout, &stderr)

	// Server goes away and never comes back.
	srvCleanup()
	srvClosed = true

	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	// 1s shutdown budget + one 0.5s check interval + generous slack.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case <-waitErr:
		// Exit status is not asserted: with the server gone the claim loop
		// ends on an error and Run exits nonzero, which is correct.
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("worker subprocess did not exit after SIGTERM with the server unreachable; stdout=%s stderr=%s", &stdout, &stderr)
	}
}

// failingTaskTypeToml exits with a distinctive non-zero status, so the
// test can tell "the task failed" from "the task failed *this* way".
const failingTaskTypeToml = `
tags = ["exec:bash", "os:unix"]
timeout = 10
command = "echo 'about to fail'; exit 3"
executor = "bash"
`

// TestProcessOne_ExitCode covers the exit code plumbing end to end
// (turtlemonvh/blanket#27): cmd.Wait() -> processExitCode ->
// tasks.MarkAsFinished -> PUT /task/:id/finish?exitCode=N -> the stored
// task record. Before this, a script exiting 3 and one exiting 1 were
// indistinguishable to any caller.
func TestProcessOne_ExitCode(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("failing_task", failingTaskTypeToml)
	h.writeTaskType("echo_task", testTaskTypeToml)

	// A task that fails reports its own exit status, not just ERROR.
	h.submit("failing_task")
	claimed := h.claim()
	_ = h.work.ProcessOne(&claimed) // returns cmd.Wait()'s error

	failed := h.fetch(claimed.Id)
	assert.Equal(t, "ERROR", failed.State)
	if assert.NotNil(t, failed.ExitCode, "a task that ran to completion should report an exit code") {
		assert.Equal(t, 3, *failed.ExitCode)
	}

	// A successful task reports 0 -- distinguishable from "unknown"
	// precisely because the field is a pointer.
	h.submit("echo_task")
	ok := h.claim()
	assert.NoError(t, h.work.ProcessOne(&ok))

	succeeded := h.fetch(ok.Id)
	assert.Equal(t, "SUCCESS", succeeded.State)
	if assert.NotNil(t, succeeded.ExitCode) {
		assert.Equal(t, 0, *succeeded.ExitCode)
	}
}

// TestProcessOne_TimeoutHasNoExitCode: a task the worker killed was
// terminated by a signal, so it has no exit status of its own to report.
// It stays null rather than being reported as -1.
func TestProcessOne_TimeoutHasNoExitCode(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()

	h.writeTaskType("slow_task", timeoutTaskTypeToml)

	h.submit("slow_task")
	claimed := h.claim()
	_ = h.work.ProcessOne(&claimed)

	final := h.fetch(claimed.Id)
	assert.Equal(t, "TIMEDOUT", final.State)
	assert.Nil(t, final.ExitCode, "a signal-killed task has no exit code of its own")
}
