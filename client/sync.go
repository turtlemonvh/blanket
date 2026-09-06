package client

/*
Synchronous submission -- turtlemonvh/blanket#27.

SubmitTaskAndWait and SubmitTaskAndStream are the client half of
POST /task/?wait and POST /task/?wait&stream. They decode the server's
own payload and event types (server.CompletionPayload,
server.StateEvent / LogEvent / ResultEvent) rather than redeclaring them
here: the point of the shared encoder is that there is exactly one wire
format, and a private copy of the structs in this package would be the
first thing to drift. The import direction is safe -- server does not
import client, and both already end up in the same binary.
*/

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/turtlemonvh/blanket/server"
)

// WaitOptions are the synchronous-submission knobs shared by
// SubmitTaskAndWait and SubmitTaskAndStream.
type WaitOptions struct {
	// Wait is the value sent as ?wait: a Go duration ("30s"), a bare
	// number of seconds, or "" for a bare ?wait (which the server reads
	// as its configured tasks.sync.defaultWait).
	Wait string
}

// WaitResult is the outcome of a synchronous submit, whichever mode
// produced it.
type WaitResult struct {
	// Payload is the completion payload -- the response body in blocking
	// mode, the terminal result event's data in streaming mode. Nil only
	// when the server never produced one.
	Payload *server.CompletionPayload
	// TimedOut is true when the wait expired with the task still
	// running: a 504 in blocking mode, a result event with waitOutcome
	// "wait_timeout" in streaming mode.
	TimedOut bool
	// Timeout carries the 504 body, when the server sent one.
	Timeout *server.WaitTimeoutBody
	// Status is the HTTP status of the submission.
	Status int
}

// TaskId returns the submitted task's id, from whichever half of the
// result carries it.
func (r WaitResult) TaskId() string {
	if r.Payload != nil {
		return r.Payload.Task.Id.Hex()
	}
	if r.Timeout != nil {
		return r.Timeout.Id
	}
	return ""
}

// waitURL builds the POST /task/ url for a synchronous submit. A bare
// `?wait` with no value is what the server reads as "use the configured
// default", so an empty opts.Wait must not become `wait=`.
func waitURL(port int, opts WaitOptions, stream bool) string {
	q := "wait"
	if opts.Wait != "" {
		q = "wait=" + url.QueryEscape(opts.Wait)
	}
	if stream {
		q += "&stream"
	}
	return fmt.Sprintf("http://localhost:%d/task/?%s", port, q)
}

// submitBody builds the JSON body for a submit.
//
// An empty environment is omitted rather than sent as {}: POST /task/
// rejects a present-but-empty "environment" as malformed (it can't tell
// "{}" from a map of the wrong shape), so sending one would make
// `blanket submit -t x --wait` fail on every task type with no env vars.
func submitBody(taskType string, env map[string]interface{}) ([]byte, error) {
	body := map[string]interface{}{"type": taskType}
	if len(env) > 0 {
		body["environment"] = env
	}
	return json.Marshal(body)
}

// SubmitTaskAndWait submits a task and blocks until it finishes, the
// server's wait budget expires, or the request fails.
//
// The returned error is reserved for the *call* not working (transport
// failure, a 400, an undecodable body). A task that itself failed is a
// successful call carrying a non-SUCCESS state in the payload -- the
// same contract the HTTP endpoint has.
func SubmitTaskAndWait(taskType string, env map[string]interface{}, port int, opts WaitOptions) (WaitResult, error) {
	res := WaitResult{}

	bts, err := submitBody(taskType, env)
	if err != nil {
		return res, err
	}

	resp, err := http.Post(waitURL(port, opts, false), "application/json", bytes.NewBuffer(bts))
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return res, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusBadGateway:
		// 502 is fail_on_error's "the task failed"; the body is the same
		// completion payload either way.
		var payload server.CompletionPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			return res, fmt.Errorf("could not decode the server's completion payload: %w", err)
		}
		res.Payload = &payload
		return res, nil
	case http.StatusGatewayTimeout:
		var timeout server.WaitTimeoutBody
		if err := json.Unmarshal(body, &timeout); err != nil {
			return res, fmt.Errorf("could not decode the server's wait-timeout response: %w", err)
		}
		res.TimedOut = true
		res.Timeout = &timeout
		return res, nil
	default:
		return res, fmt.Errorf("%s", serverErrorMessage(body, resp.StatusCode))
	}
}

// StreamCallbacks receives a streaming submit's events as they arrive.
// Any callback may be nil; returning an error from one aborts the stream
// and is returned from SubmitTaskAndStream.
type StreamCallbacks struct {
	OnState  func(server.StateEvent) error
	OnLog    func(server.LogEvent) error
	OnResult func(server.ResultEvent) error
}

// streamLineLimit is the biggest single NDJSON event this client
// accepts. The terminal result event carries both output tails
// (tasks.sync.maxLogLines lines each) plus the parsed result artifact
// (tasks.sync.maxResultBytes, 1 MiB by default), so bufio.Scanner's
// 64 KiB default token size is far too small.
const streamLineLimit = 8 * 1024 * 1024

// SubmitTaskAndStream submits a task with ?wait&stream and consumes the
// NDJSON event stream, invoking cb for each event as it arrives. It
// returns when the stream ends -- normally at the terminal result event,
// whose payload is also returned in the WaitResult.
func SubmitTaskAndStream(taskType string, env map[string]interface{}, port int, opts WaitOptions, cb StreamCallbacks) (WaitResult, error) {
	res := WaitResult{}

	bts, err := submitBody(taskType, env)
	if err != nil {
		return res, err
	}

	req, err := http.NewRequest("POST", waitURL(port, opts, true), bytes.NewBuffer(bts))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", server.ContentTypeNDJSON)

	// Deliberately no client-side timeout: the server's own wait budget
	// bounds the call, and a shorter one here would turn a slow task
	// into a confusing transport error.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	res.Status = resp.StatusCode

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return res, fmt.Errorf("%s", serverErrorMessage(body, resp.StatusCode))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), streamLineLimit)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var envelope server.EventEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return res, fmt.Errorf("could not decode a task event: %w", err)
		}

		switch envelope.Type {
		case server.EventTypeState:
			var ev server.StateEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				return res, err
			}
			if cb.OnState != nil {
				if err := cb.OnState(ev); err != nil {
					return res, err
				}
			}
		case server.EventTypeLog:
			var ev server.LogEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				return res, err
			}
			if cb.OnLog != nil {
				if err := cb.OnLog(ev); err != nil {
					return res, err
				}
			}
		case server.EventTypeResult:
			var ev server.ResultEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				return res, err
			}
			payload := ev.CompletionPayload
			res.Payload = &payload
			res.TimedOut = payload.WaitOutcome == server.WaitOutcomeTimeout
			if cb.OnResult != nil {
				if err := cb.OnResult(ev); err != nil {
					return res, err
				}
			}
		default:
			// An unknown event type is skipped rather than treated as an
			// error, so a newer server can add one without breaking an
			// older CLI.
		}
	}
	if err := scanner.Err(); err != nil {
		return res, err
	}

	if res.Payload == nil {
		return res, fmt.Errorf("task event stream ended without a result event")
	}
	return res, nil
}

// serverErrorMessage pulls the message out of blanket's usual
// {"error": "..."} body, falling back to the raw body plus status.
func serverErrorMessage(body []byte, status int) string {
	var errBody struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &errBody); err == nil && errBody.Error != "" {
		return errBody.Error
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Sprintf("server returned HTTP %d", status)
	}
	return fmt.Sprintf("server returned HTTP %d: %s", status, trimmed)
}
