package server

// Synchronous ("blocking") task submission -- turtlemonvh/blanket#27.
//
// POST /task/?wait turns the normal fire-and-forget submit into a single
// call that returns the task's end state, exit code, output tail, and
// parsed result artifact. Nothing about how the task runs changes: it is
// saved, queued, claimed, and executed by a worker exactly as before, and
// the handler simply waits for the record to reach a terminal state
// before responding.
//
// This file holds the pieces that are independent of the response
// framing -- the wait loop, the completion payload and its builder, and
// the result_file reader -- so the streaming variant (&stream, PR 2 of
// the issue) can emit the identical object as its terminal `result`
// event rather than growing a second, drifting encoder.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// waitOutcome values. A blocking caller can also tell these apart from
// the HTTP status (200 vs 504), but the streaming variant cannot -- once
// the body has started, the status is already sent -- so the distinction
// lives in the payload as well.
const (
	// WaitOutcomeCompleted: the task reached a terminal state within the
	// caller's wait budget.
	WaitOutcomeCompleted = "completed"
	// WaitOutcomeTimeout: the wait expired with the task still live. The
	// task is untouched and keeps running.
	WaitOutcomeTimeout = "wait_timeout"
)

// Fallbacks for the tasks.sync.* config keys, used when a key is unset or
// nonsensical (<= 0). command/root.go sets the same values as viper
// defaults for a real server; these keep the handler sane in tests and in
// any embedding that doesn't go through InitializeConfig.
const (
	DefaultSyncWait           = 30 * time.Second
	DefaultSyncMaxWait        = 300 * time.Second
	DefaultSyncMaxLogLines    = 200
	DefaultSyncMaxResultBytes = int64(1048576)

	// syncPollInterval is the fallback ticker in the wait loop. EventHub
	// delivery is best-effort (a non-blocking send to a one-buffered
	// channel), so the loop re-reads the task on a timer too rather than
	// trusting a notification to always arrive. Scaled by timeMultiplier
	// like the other time-based paths.
	syncPollInterval = time.Second
)

func syncDefaultWait() time.Duration {
	if d := viper.GetDuration("tasks.sync.defaultWait"); d > 0 {
		return d
	}
	return DefaultSyncWait
}

func syncMaxWait() time.Duration {
	if d := viper.GetDuration("tasks.sync.maxWait"); d > 0 {
		return d
	}
	return DefaultSyncMaxWait
}

func syncMaxLogLines() int {
	if n := viper.GetInt("tasks.sync.maxLogLines"); n > 0 {
		return n
	}
	return DefaultSyncMaxLogLines
}

func syncMaxResultBytes() int64 {
	if n := viper.GetInt64("tasks.sync.maxResultBytes"); n > 0 {
		return n
	}
	return DefaultSyncMaxResultBytes
}

// timeMultiplier resolves the test-speed knob the same way the rest of
// the server does: the explicitly-configured ServerConfig field wins, and
// viper is the fallback for a config built without one.
func (s *ServerConfig) timeMultiplier() float64 {
	if s.TimeMultiplier > 0 {
		return s.TimeMultiplier
	}
	if m := viper.GetFloat64("timeMultiplier"); m > 0 {
		return m
	}
	return 1.0
}

// CompletionPayload is the body of a successful synchronous submit. It is
// also (PR 2) the data of the terminal `result` event in streaming mode,
// byte-identical, so a client that can parse one can parse the other.
type CompletionPayload struct {
	// Task is the existing tasks.Task JSON verbatim -- same field names,
	// same computed scheduleDescription -- plus the new exitCode.
	Task tasks.Task `json:"task"`
	// WaitOutcome is WaitOutcomeCompleted or WaitOutcomeTimeout.
	WaitOutcome string `json:"waitOutcome"`
	// Stdout/Stderr are the last tasks.sync.maxLogLines lines of each
	// stream, with the matching *Truncated flag set when earlier lines
	// were dropped.
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdoutTruncated"`
	StderrTruncated bool   `json:"stderrTruncated"`
	// Result is the parsed contents of the task type's declared
	// result_file, or null when the type declares none, the file is
	// absent, or it could not be read.
	Result interface{} `json:"result"`
	// ResultError describes why Result is null despite a declared
	// result_file (unparseable, oversized, unreadable). null when there
	// was nothing wrong -- an absent file is a normal outcome, not an
	// error.
	ResultError *string `json:"resultError"`
}

// waitTimeoutBody is the 504 body: enough for the caller to pick the task
// back up asynchronously, which is exactly what the wait expiring means.
type waitTimeoutBody struct {
	Id          string `json:"id"`
	State       string `json:"state"`
	WaitOutcome string `json:"waitOutcome"`
	PollUrl     string `json:"pollUrl"`
	Error       string `json:"error"`
}

// syncWaitParams is the parsed form of the ?wait / ?fail_on_error query
// parameters on POST /task/.
type syncWaitParams struct {
	// Requested is false when no ?wait was passed at all -- the endpoint
	// then behaves exactly as it always has (201, return immediately).
	Requested bool
	// Wait is the caller's budget, already validated against
	// tasks.sync.maxWait.
	Wait time.Duration
	// FailOnError makes a non-SUCCESS terminal state answer 502 instead
	// of 200. Opt-in, so `curl --fail` has a way to notice a failed task
	// without changing the default contract for everyone else.
	FailOnError bool
}

// parseSyncWaitParams reads ?wait and ?fail_on_error off a request.
//
// Accepted forms for wait: bare `?wait` (tasks.sync.defaultWait), a Go
// duration (`?wait=30s`, `?wait=2m`), or a bare integer read as seconds
// (`?wait=30`). Over tasks.sync.maxWait is an error rather than a silent
// clamp -- a caller should never believe it waited longer than it did.
//
// fail_on_error accepts the usual boolean spellings, plus bare
// `?fail_on_error` (no value) as true, matching how bare `?wait` works.
func parseSyncWaitParams(c *gin.Context) (syncWaitParams, error) {
	p := syncWaitParams{}

	if raw, ok := c.GetQuery("wait"); ok {
		p.Requested = true
		switch {
		case strings.TrimSpace(raw) == "":
			p.Wait = syncDefaultWait()
		default:
			d, err := parseWaitDuration(raw)
			if err != nil {
				return p, err
			}
			if d <= 0 {
				return p, fmt.Errorf("invalid 'wait' value %q: must be a positive duration", raw)
			}
			p.Wait = d
		}
		if max := syncMaxWait(); p.Wait > max {
			return p, fmt.Errorf("'wait' of %s exceeds the server's tasks.sync.maxWait of %s; submit without ?wait and poll GET /task/:id instead", p.Wait, max)
		}
	}

	if raw, ok := c.GetQuery("fail_on_error"); ok {
		if strings.TrimSpace(raw) == "" {
			p.FailOnError = true
		} else {
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return p, fmt.Errorf("invalid 'fail_on_error' value %q: must be true or false", raw)
			}
			p.FailOnError = b
		}
	}

	return p, nil
}

// parseWaitDuration accepts a Go duration string, or a bare number read
// as seconds (?wait=30 is a natural thing for a caller to write, and
// there is no other unit it could plausibly mean).
func parseWaitDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	if secs, err := strconv.ParseFloat(raw, 64); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("invalid 'wait' value %q: expected a duration like '30s' or a number of seconds", raw)
}

// waitForTerminalState blocks until taskId reaches a terminal state, the
// caller's budget expires, or the client goes away.
//
// The subscription must already exist (and must have been taken *before*
// the task was enqueued) so a task that finishes unusually fast can't slip
// through between the enqueue and the subscribe. EventHub carries no
// payload and is a global broadcast, so every wake -- notification,
// ticker, or the initial pass -- re-reads the task and decides from the
// record itself; the channel is only a hint that something changed.
//
// Returns the final task and WaitOutcomeCompleted, or WaitOutcomeTimeout
// with the task as last seen. A disconnected client returns
// (task, "", true): the caller should write nothing and leave the task
// alone, exactly as if the submit had been asynchronous.
func (s *ServerConfig) waitForTerminalState(c *gin.Context, sub chan struct{}, taskId objectid.ObjectId, wait time.Duration) (tasks.Task, string, bool) {
	mult := s.timeMultiplier()

	ticker := time.NewTicker(time.Duration(float64(syncPollInterval) * mult))
	defer ticker.Stop()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	var last tasks.Task
	ctx := c.Request.Context()

	for {
		task, err := s.DB.GetTask(taskId)
		if err == nil {
			last = task
			if tasks.IsTerminalState(task.State) {
				return task, WaitOutcomeCompleted, false
			}
		} else {
			// A task deleted out from under the waiter can't ever reach a
			// terminal state; report what we last saw when the wait runs
			// out rather than spinning on the error.
			log.WithFields(log.Fields{
				"err":    err.Error(),
				"taskId": taskId.Hex(),
			}).Debug("error re-reading task while waiting for it to finish")
		}

		select {
		case <-sub:
		case <-ticker.C:
		case <-ctx.Done():
			return last, "", true
		case <-deadline.C:
			return last, WaitOutcomeTimeout, false
		}
	}
}

// buildCompletionPayload assembles the response body for a task that has
// reached a terminal state: the task record, the tail of both output
// streams, and the parsed result artifact.
//
// Output is read from the files under the task's ResultDir --
// blanket.stdout.log and blanket.stderr.log, both written by the worker
// in SetupExecutionDirectory. stdout has a tailing endpoint already
// (/task/:id/log/tail); stderr is only reachable as a static file under
// /results today, so it is read from disk here the same way.
//
// Never fails: a missing or unreadable log file is reported as empty
// output, since a task can legitimately finish having written neither.
func (s *ServerConfig) buildCompletionPayload(task tasks.Task, outcome string) CompletionPayload {
	maxLines := syncMaxLogLines()

	stdout, stdoutTruncated, err := tailLinesTruncated(path.Join(task.ResultDir, "blanket.stdout.log"), maxLines)
	if err != nil {
		stdout, stdoutTruncated = "", false
	}
	stderr, stderrTruncated, err := tailLinesTruncated(path.Join(task.ResultDir, "blanket.stderr.log"), maxLines)
	if err != nil {
		stderr, stderrTruncated = "", false
	}

	result, resultErr := readTaskResult(task, syncMaxResultBytes())

	payload := CompletionPayload{
		Task:            task,
		WaitOutcome:     outcome,
		Stdout:          stdout,
		Stderr:          stderr,
		StdoutTruncated: stdoutTruncated,
		StderrTruncated: stderrTruncated,
		Result:          result,
	}
	if resultErr != "" {
		payload.ResultError = &resultErr
	}
	return payload
}

// readTaskResult reads, size-checks, and JSON-parses the file the task's
// type declared as its `result_file`.
//
// Returns (nil, "") when there is nothing to report: the type declares no
// result_file, or the file simply isn't there -- a task that failed
// before writing its result is a normal outcome, not an error. Returns
// (nil, message) when a declared file exists but couldn't be turned into
// a result, so a malformed result never masquerades as an absent one.
func readTaskResult(task tasks.Task, maxBytes int64) (interface{}, string) {
	tt, err := task.GetTaskType()
	if err != nil {
		// The type may have been edited or removed while the task ran.
		// Not fatal: there's simply no declared result to read.
		return nil, ""
	}

	rel, err := tt.ResultFile()
	if err != nil {
		return nil, err.Error()
	}
	if rel == "" {
		return nil, ""
	}
	return readResultFileAt(task.ResultDir, rel, maxBytes)
}

// readResultFileAt is readTaskResult once the declared path is known:
// contain it to resultDir, size-check it, read it, parse it. Split out so
// the containment rule can be exercised directly -- the loader rejects an
// escaping result_file long before this point, which is exactly why the
// check here needs its own test.
func readResultFileAt(dir, rel string, maxBytes int64) (interface{}, string) {
	// The declared path is validated again here, not just at task-type
	// load time: this is a config-supplied path being joined to a
	// server-side directory and read by the server process.
	cleanRel, err := tasks.CleanResultFile(rel)
	if err != nil {
		return nil, err.Error()
	}
	if cleanRel == "" {
		return nil, ""
	}
	rel = cleanRel

	resultDir := filepath.Clean(dir)
	full := filepath.Clean(filepath.Join(resultDir, filepath.FromSlash(rel)))
	if full != resultDir && !strings.HasPrefix(full, resultDir+string(os.PathSeparator)) {
		return nil, fmt.Sprintf("result_file %q resolves outside the task's result directory", rel)
	}

	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ""
		}
		return nil, fmt.Sprintf("could not read result_file %q: %s", rel, err.Error())
	}
	if fi.IsDir() {
		return nil, fmt.Sprintf("result_file %q is a directory, not a file", rel)
	}
	if fi.Size() > maxBytes {
		return nil, fmt.Sprintf("result_file %q is %d bytes, larger than tasks.sync.maxResultBytes (%d)", rel, fi.Size(), maxBytes)
	}

	contents, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Sprintf("could not read result_file %q: %s", rel, err.Error())
	}

	var parsed interface{}
	if err := json.Unmarshal(contents, &parsed); err != nil {
		return nil, fmt.Sprintf("could not parse result_file %q as JSON: %s", rel, err.Error())
	}
	return parsed, ""
}

// respondAfterWait writes the response for a synchronous submit whose
// wait has ended, mapping the outcome onto a status code:
//
//	completed                                  -> 200 + completion payload
//	completed, non-SUCCESS, fail_on_error=true -> 502 + the same payload
//	wait expired                               -> 504 + poll info
//
// Note that a failed *task* is a 200 by default: an HTTP status describes
// whether the API call worked, and the task's own outcome is carried by
// state / exitCode in the body. fail_on_error is the opt-in for callers
// (shell scripts using `curl --fail`, mostly) that would rather have the
// status carry both.
func (s *ServerConfig) respondAfterWait(c *gin.Context, task tasks.Task, outcome string, p syncWaitParams) {
	if outcome == WaitOutcomeTimeout {
		c.JSON(http.StatusGatewayTimeout, waitTimeoutBody{
			Id:          task.Id.Hex(),
			State:       task.State,
			WaitOutcome: WaitOutcomeTimeout,
			PollUrl:     fmt.Sprintf("/task/%s", task.Id.Hex()),
			Error:       fmt.Sprintf("task did not reach a terminal state within %s; it is still running and can be polled at the url in 'pollUrl'", p.Wait),
		})
		return
	}

	payload := s.buildCompletionPayload(task, outcome)
	status := http.StatusOK
	if p.FailOnError && task.State != "SUCCESS" {
		status = http.StatusBadGateway
	}
	c.JSON(status, payload)
}
