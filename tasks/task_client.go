package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/objectid"
)

// FIXME: Move to client package
// Functions that work over http to transition task state

// Retry budgets for the worker→server state transitions
// (turtlemonvh/blanket#23 phase 1). These are unscaled constants; the
// timeMultiplier is applied inside lib/httpx via lib/timing, so a
// compressed test run compresses them too.
//
// They are generous on purpose. Losing a terminal state is the single most
// expensive failure in the system — the task is stranded in RUNNING
// forever and its result is lost — and the worker is idle anyway while it
// retries, so a long budget costs nothing but covers a whole server
// restart. Finishing gets more budget than running for the same reason.
//
// They're vars rather than consts so tests can shorten them without
// dragging every other timeMultiplier-scaled duration along with them.
var (
	// RunRetryDeadline bounds retries of PUT /task/:id/run.
	RunRetryDeadline = 120 * time.Second
	// FinishRetryDeadline bounds retries of PUT /task/:id/finish.
	FinishRetryDeadline = 300 * time.Second
	// TimeoutFinishDeadline bounds the PUT /task/:id/finish that reports
	// TIMEDOUT. It is deliberately much shorter than FinishRetryDeadline:
	// that call is made by the monitoring goroutine *before* it kills an
	// overrunning child process, so a long budget would leave a task that
	// has already blown its timeout running for the length of the budget.
	TimeoutFinishDeadline = 30 * time.Second
)

func taskURL(taskId objectid.ObjectId, suffix string) string {
	return fmt.Sprintf("http://localhost:%d/task/%s%s", viper.GetInt("port"), taskId.Hex(), suffix)
}

// Refresh information about this task by pulling from the blanket server.
//
// Deliberately does not retry: every caller is in a polling loop that will
// try again on its own schedule, and the callers that matter (the task
// monitoring goroutine) must fail open on an error rather than wait.
func (t *Task) Refresh() error {
	res, err := httpx.DoOnce(context.Background(), "GET", taskURL(t.Id, ""), nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return err
	}
	return json.Unmarshal(res.Body, t)
}

// FIXME: Should operate on a task object and set the property on it
// Should only be called by worker
//
// runId is the fencing token for this execution attempt; see Task.RunId. It
// is passed as a query parameter alongside the other run fields.
func MarkAsRunning(t *Task, runId string, extraVars map[string]string) error {
	urlParams := url.Values{}
	urlParams.Set("state", "RUNNING")
	for k, v := range extraVars {
		urlParams.Set(k, v)
	}
	urlParams.Set("runId", runId)

	reqURL := taskURL(t.Id, "/run") + "?" + urlParams.Encode()
	_, err := httpx.Do(context.Background(), "PUT", reqURL, nil, httpx.Policy{Deadline: RunRetryDeadline})
	return err
}

// Should only be called by worker
// Set task to one of the following states: ERROR/SUCCESS/TIMEDOUT/STOPPED
//
// runId is the fencing token for this execution attempt; see Task.RunId.
// exitCode is the process exit status, or nil when there isn't one to
// report (the process never started, or was killed by a signal). Both ride
// along as query parameters the same way MarkAsRunning passes timeout /
// pid / typeDigest.
//
// A 409 means another runner owns this task (mismatched RunId) and is
// returned without retrying; so is a 400 or 404. Anything transient — a
// refused connection, a reset, a 5xx — is retried with full-jitter backoff
// until the deadline.
func MarkAsFinished(t *Task, state string, runId string, exitCode *int) error {
	return MarkAsFinishedWithin(t, state, runId, exitCode, FinishRetryDeadline)
}

// MarkAsFinishedWithin is MarkAsFinished with an explicit retry budget.
func MarkAsFinishedWithin(t *Task, state string, runId string, exitCode *int, deadline time.Duration) error {
	urlParams := url.Values{}
	urlParams.Set("state", state)
	urlParams.Set("runId", runId)
	if exitCode != nil {
		urlParams.Set("exitCode", fmt.Sprintf("%d", *exitCode))
	}

	reqURL := taskURL(t.Id, "/finish") + "?" + urlParams.Encode()
	_, err := httpx.Do(context.Background(), "PUT", reqURL, nil, httpx.Policy{Deadline: deadline})
	return err
}

// Find the oldest task we are eligible to run
func MarkAsClaimed(workerId objectid.ObjectId) (Task, error) {
	// Call the REST api and get a task with the required tags
	// The worker needs to make sure it has all the tags of whatever task it requests
	reqURL := fmt.Sprintf("http://localhost:%d/task/claim/%s", viper.GetInt("port"), workerId.Hex())

	// No retry here: the worker's claim loop is itself the retry, with its
	// own jittered backoff (see WorkerConf.ProcessTasks).
	res, err := httpx.DoOnce(context.Background(), "POST", reqURL, nil, httpx.DefaultRequestTimeout)
	if err != nil {
		if httpx.StatusCodeOf(err) == http.StatusNotFound {
			log.WithFields(log.Fields{
				"resp": err.Error(),
			}).Warn("Claimed task was deleted or stopped before we could process it; will retry")
			return Task{}, nil
		}
		if code := httpx.StatusCodeOf(err); code != 0 {
			log.WithFields(log.Fields{
				"resp":       err.Error(),
				"statusCode": code,
			}).Error("Problem claiming task")
		}
		return Task{}, err
	}

	if res.StatusCode == http.StatusNoContent {
		// Empty queue for this worker — not an error, just poll again later.
		return Task{}, nil
	}

	// Try to marshall this into a task object
	var t Task
	if err := json.Unmarshal(res.Body, &t); err != nil {
		return Task{}, fmt.Errorf("Error decoding claimed task; possible data corruption or server/worker version mismatch :: %s", err.Error())
	}

	return t, nil
}
