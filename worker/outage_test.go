// Outage injection for the in-process worker harness
// (turtlemonvh/blanket#23 phase 1).
//
// The worker's resilience story is entirely about what happens when the
// server goes away mid-transition: the retry loop, the response
// classification, the fencing token, and the outcome journal all only
// matter in that window. Those were previously untestable without a
// subprocess harness and real sleeps.
//
// Hijacking the connection and closing it produces a transport error
// indistinguishable from a dead server — connection reset, from inside one
// process, in microseconds. That plus a "return 5xx" mode covers both
// halves of the classifier, so every row of the idempotency table and every
// retry path becomes a fast, deterministic test that runs under -race.
package worker_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

type outageMode int

const (
	// outageNone lets everything through.
	outageNone outageMode = iota
	// outageHijack takes the connection over and closes it without a
	// response — what a worker sees when the server process dies or is
	// restarted mid-request.
	outageHijack
	// outageStatus answers 500, the "server is up but broken" case the
	// classifier must retry (and which the pre-fix client read as success).
	outageStatus
)

// outageInjector breaks a bounded number of requests matching a path
// substring. Safe for concurrent use: the harness's handler runs on the
// server's goroutines while the test drives it from its own.
type outageInjector struct {
	mu        sync.Mutex
	mode      outageMode
	match     string // substring of the request path; "" matches everything
	remaining int    // requests left to break; -1 means "until cleared"
	broken    int
}

// inject starts breaking the next n requests whose path contains match.
// n < 0 breaks them until clear() is called.
func (o *outageInjector) inject(match string, mode outageMode, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode = mode
	o.match = match
	o.remaining = n
	o.broken = 0
}

func (o *outageInjector) clear() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode = outageNone
	o.remaining = 0
}

// brokenCount reports how many requests have been broken since the last
// inject.
func (o *outageInjector) brokenCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.broken
}

// intercept reports whether it handled (broke) the request.
func (o *outageInjector) intercept(w http.ResponseWriter, r *http.Request) bool {
	o.mu.Lock()
	if o.mode == outageNone || o.remaining == 0 || !strings.Contains(r.URL.Path, o.match) {
		o.mu.Unlock()
		return false
	}
	mode := o.mode
	if o.remaining > 0 {
		o.remaining--
	}
	o.broken++
	o.mu.Unlock()

	switch mode {
	case outageHijack:
		hj, ok := w.(http.Hijacker)
		if !ok {
			// Shouldn't happen with net/http's default server, but
			// degrade to the status mode rather than silently passing the
			// request through and making a test lie.
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return true
		}
		conn.Close()
	default:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "injected outage"}`))
	}
	return true
}

// withShortRetryDeadlines compresses the worker→server retry budgets for
// the duration of a test. The production values are minutes long by design
// (see tasks/task_client.go); a test that wants to watch one run out needs
// them to be milliseconds, and doing it here rather than through
// timeMultiplier keeps the task's own timeout and the poll interval at
// their normal values.
func withShortRetryDeadlines(t *testing.T, d time.Duration) {
	t.Helper()
	run, finish, timeout := tasks.RunRetryDeadline, tasks.FinishRetryDeadline, tasks.TimeoutFinishDeadline
	tasks.RunRetryDeadline = d
	tasks.FinishRetryDeadline = d
	tasks.TimeoutFinishDeadline = d
	t.Cleanup(func() {
		tasks.RunRetryDeadline = run
		tasks.FinishRetryDeadline = finish
		tasks.TimeoutFinishDeadline = timeout
	})
}

// journalPath is where the worker writes a task's outcome journal.
func (h *workerHarness) journalPath(taskId objectid.ObjectId) string {
	return filepath.Join(h.resultsDir, taskId.Hex(), worker.OutcomeJournalFilename)
}

func (h *workerHarness) readJournal(taskId objectid.ObjectId) (*worker.OutcomeJournal, error) {
	return worker.ReadOutcomeJournal(filepath.Join(h.resultsDir, taskId.Hex()))
}

// --- retry + classification ---

// TestProcessOne_RetriesRunThroughOutage: the RUNNING transition is lost to
// a dead connection twice before it lands. Pre-fix there was no retry at
// all, so this task would have been abandoned with the server still
// believing it was CLAIMED — and its child process left running.
func TestProcessOne_RetriesRunThroughOutage(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	h.writeTaskType("echo_task", testTaskTypeToml)

	submitted := h.submit("echo_task")
	claimed := h.claim()

	h.outage.inject("/run", outageHijack, 2)
	assert.NoError(t, h.work.ProcessOne(&claimed))
	assert.Equal(t, 2, h.outage.brokenCount())

	final := h.fetch(submitted.Id)
	assert.Equal(t, "SUCCESS", final.State)
	assert.NotEmpty(t, final.RunId, "the run should have recorded a fencing token")
}

// TestProcessOne_RetriesFinishThroughOutage is the same for the terminal
// transition, and through 5xx rather than a dead connection — the case the
// pre-fix client read as success, silently losing the task's outcome.
func TestProcessOne_RetriesFinishThroughOutage(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	h.writeTaskType("echo_task", testTaskTypeToml)

	submitted := h.submit("echo_task")
	claimed := h.claim()

	h.outage.inject("/finish", outageStatus, 3)
	assert.NoError(t, h.work.ProcessOne(&claimed))
	assert.Equal(t, 3, h.outage.brokenCount())

	final := h.fetch(submitted.Id)
	assert.Equal(t, "SUCCESS", final.State)

	// Acknowledged, so the journal is gone.
	_, err := os.Stat(h.journalPath(submitted.Id))
	assert.True(t, os.IsNotExist(err), "journal should be removed after a successful finish")
}

// TestProcessOne_FinishFailureIsReported: a server that never comes back
// must surface as an error from ProcessOne, not a silent success. This is
// the "a 500 reads as success" defect, from the worker's side.
func TestProcessOne_FinishFailureIsReported(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	withShortRetryDeadlines(t, 500*time.Millisecond)
	h.writeTaskType("echo_task", testTaskTypeToml)

	submitted := h.submit("echo_task")
	claimed := h.claim()

	h.outage.inject("/finish", outageStatus, -1)
	started := time.Now()
	err := h.work.ProcessOne(&claimed)
	elapsed := time.Since(started)

	assert.Error(t, err, "an unacknowledged finish must not look like success")
	assert.Greater(t, h.outage.brokenCount(), 1, "should have retried more than once")
	assert.Less(t, elapsed, 30*time.Second, "should have given up at the deadline")

	// The task is still RUNNING as far as the server knows; that's exactly
	// the state the phase 3 reaper resolves, using the journal below.
	final := h.fetch(submitted.Id)
	assert.Equal(t, "RUNNING", final.State)
}

// TestMarkAsFinished_DoesNotRetryConflict: a 409 means another run owns the
// task, and no amount of retrying will change that. It must come back
// immediately rather than burning the (minutes-long) deadline.
func TestMarkAsFinished_DoesNotRetryConflict(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	h.writeTaskType("echo_task", testTaskTypeToml)

	submitted := h.submit("echo_task")
	claimed := h.claim()

	// Put the task in RUNNING under run id "RUN1".
	require.NoError(t, tasks.MarkAsRunning(&claimed, "RUN1", map[string]string{"timeout": "10"}))

	started := time.Now()
	err := tasks.MarkAsFinished(&claimed, "SUCCESS", "RUN2", nil)
	elapsed := time.Since(started)

	assert.Error(t, err)
	assert.Less(t, elapsed, 5*time.Second, "a 409 must not be retried")

	final := h.fetch(submitted.Id)
	assert.Equal(t, "RUNNING", final.State)
	assert.Equal(t, "RUN1", final.RunId)
}

// --- the outcome journal ---

// TestProcessOne_JournalLifecycle watches the journal through a whole run:
// present and "running" while the child is alive, gone once the server has
// acknowledged the finish.
func TestProcessOne_JournalLifecycle(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	h.writeTaskType("long_task", longRunningTaskTypeToml)

	submitted := h.submit("long_task")
	claimed := h.claim()

	done := make(chan error, 1)
	go func() { done <- h.work.ProcessOne(&claimed) }()

	var mid *worker.OutcomeJournal
	require.Eventually(t, func() bool {
		j, err := h.readJournal(submitted.Id)
		if err != nil {
			return false
		}
		mid = j
		return true
	}, 10*time.Second, 50*time.Millisecond, "journal was never written while the task was running")

	assert.Equal(t, worker.OutcomeStateRunning, mid.State)
	assert.Equal(t, submitted.Id.Hex(), mid.TaskId)
	assert.Equal(t, h.work.Id.Hex(), mid.WorkerId)
	assert.NotEmpty(t, mid.RunId)
	assert.NotZero(t, mid.Pid)
	assert.NotZero(t, mid.StartedTs)
	assert.Zero(t, mid.ExitedTs)

	h.cancel(submitted.Id)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ProcessOne did not return after cancel")
	}

	// A user's STOPPED beats the worker's late ERROR, and the worker still
	// treats the finish as acknowledged — so the journal is cleaned up
	// rather than left behind looking like lost work.
	assert.Equal(t, "STOPPED", h.fetch(submitted.Id).State)
	_, err := os.Stat(h.journalPath(submitted.Id))
	assert.True(t, os.IsNotExist(err), "journal should be removed once the finish is acknowledged")
}

// TestProcessOne_JournalSurvivesUnreportedFinish is the journal's reason to
// exist: the child finished, the server never heard about it, and the real
// outcome — including the exit code — is on disk for the phase 3 reaper.
func TestProcessOne_JournalSurvivesUnreportedFinish(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	withShortRetryDeadlines(t, 500*time.Millisecond)
	h.writeTaskType("echo_task", testTaskTypeToml)

	submitted := h.submit("echo_task")
	claimed := h.claim()

	h.outage.inject("/finish", outageHijack, -1)
	assert.Error(t, h.work.ProcessOne(&claimed))

	j, err := h.readJournal(submitted.Id)
	require.NoError(t, err, "journal must be left in place when the finish never landed")
	assert.Equal(t, worker.OutcomeStateExited, j.State)
	assert.Equal(t, 0, j.ExitCode, "the echo task exits 0 even though we could not report it")
	assert.NotZero(t, j.ExitedTs)
	assert.NotEmpty(t, j.RunId)
	assert.Equal(t, worker.OutcomeJournalVersion, j.Version)
}

// TestProcessOne_JournalRecordsNonZeroExit: the exit code is the fact the
// reaper can't reconstruct any other way.
func TestProcessOne_JournalRecordsNonZeroExit(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	withShortRetryDeadlines(t, 500*time.Millisecond)
	h.writeTaskType("failing_task", `
tags = ["exec:bash", "os:unix"]
timeout = 10
command = "exit 3"
executor = "bash"
`)

	submitted := h.submit("failing_task")
	claimed := h.claim()

	h.outage.inject("/finish", outageHijack, -1)
	assert.Error(t, h.work.ProcessOne(&claimed))

	j, err := h.readJournal(submitted.Id)
	require.NoError(t, err)
	assert.Equal(t, worker.OutcomeStateExited, j.State)
	assert.Equal(t, 3, j.ExitCode)
}

// --- claim loop backoff ---

// TestProcessTasks_BacksOffDuringOutage: with the server answering nothing
// at all, the claim loop must keep its request rate near the check interval
// rather than spinning, and must recover once the server returns.
func TestProcessTasks_BacksOffDuringOutage(t *testing.T) {
	h := newWorkerHarness(t)
	defer h.cleanup()
	h.writeTaskType("echo_task", testTaskTypeToml)
	h.work.CheckInterval = worker.MIN_CHECK_INTERVAL_SECONDS

	h.outage.inject("", outageHijack, -1)

	done := make(chan error, 1)
	go func() { done <- h.work.ProcessTasks() }()

	time.Sleep(2 * time.Second)
	broken := h.outage.brokenCount()

	// Full jitter over a 0.5s base means the expected rate is at most a few
	// per second even at attempt 0, and it decays from there. The ceiling
	// is generous; the pre-jitter failure mode this guards against is
	// hundreds.
	assert.Less(t, broken, 40, "claim loop hammered the server during an outage")
	assert.Greater(t, broken, 0, "claim loop made no attempts at all")

	// Recovery: once the server answers again the worker picks up work.
	h.outage.clear()
	submitted := h.submit("echo_task")
	require.Eventually(t, func() bool {
		return h.fetch(submitted.Id).State == "SUCCESS"
	}, 25*time.Second, 100*time.Millisecond, "worker did not recover after the outage ended")

	h.stopWorkerViaAPI()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("ProcessTasks did not exit after stop")
	}
}

// --- outcome journal file mechanics ---

// TestOutcomeJournal_RoundTripAndRemove covers the atomic write helper on
// its own: an overwrite must replace the file rather than append to it, and
// removing a journal that isn't there must stay harmless (the finish path
// can reach the delete twice).
func TestOutcomeJournal_RoundTripAndRemove(t *testing.T) {
	dir := t.TempDir()

	j := &worker.OutcomeJournal{
		State:     worker.OutcomeStateRunning,
		RunId:     "R1",
		TaskId:    objectid.NewObjectId().Hex(),
		WorkerId:  objectid.NewObjectId().Hex(),
		Pid:       4242,
		StartedTs: time.Now().Unix(),
	}
	require.NoError(t, worker.WriteOutcomeJournal(dir, j))

	got, err := worker.ReadOutcomeJournal(dir)
	require.NoError(t, err)
	assert.Equal(t, worker.OutcomeStateRunning, got.State)
	assert.Equal(t, worker.OutcomeJournalVersion, got.Version)
	assert.Equal(t, 4242, got.Pid)

	j.State = worker.OutcomeStateExited
	j.ExitCode = 7
	j.ExitedTs = time.Now().Unix()
	require.NoError(t, worker.WriteOutcomeJournal(dir, j))

	got, err = worker.ReadOutcomeJournal(dir)
	require.NoError(t, err)
	assert.Equal(t, worker.OutcomeStateExited, got.State)
	assert.Equal(t, 7, got.ExitCode)

	// No temp files left behind by the write-then-rename.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "expected only the journal itself in %s", dir)

	require.NoError(t, worker.RemoveOutcomeJournal(dir))
	require.NoError(t, worker.RemoveOutcomeJournal(dir))
	_, err = worker.ReadOutcomeJournal(dir)
	assert.True(t, os.IsNotExist(err))
}
