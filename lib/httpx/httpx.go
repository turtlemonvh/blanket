// Package httpx is the shared HTTP client every blanket process uses to
// talk to a blanket server: the worker's task-state transitions, the
// worker's own registration/heartbeat calls, and the CLI's read calls.
//
// It exists because of two defects (turtlemonvh/blanket#23, phase 1):
//
//   - There were no HTTP client timeouts anywhere. Every call site used
//     http.DefaultClient, which has none, so a wedged server hangs a worker
//     forever and silently.
//   - Responses were not classified. A 500 from PUT /task/:id/finish read
//     as success, so a task could be reported finished when the server had
//     in fact rejected the transition — and there was no retry, so a
//     one-second outage stranded the task in RUNNING forever and leaked its
//     child process.
//
// The timeouts are deliberately transport-level: a 2s dial timeout and a 5s
// ResponseHeaderTimeout rather than a flat http.Client.Timeout, so "the
// server is wedged" (no response headers) is distinguishable from "this
// response is slow" (headers arrived, body still streaming — which is what
// the SSE log endpoints do). Callers add a per-request context deadline on
// top; see Policy.RequestTimeout.
//
// Retries use full jitter — rand(0, min(base<<n, max)) — rather than equal
// jitter or plain exponential backoff. On a server restart every worker on
// the box fails at the same instant; anything less than full jitter leaves
// them retrying in lockstep.
package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"time"

	"github.com/turtlemonvh/blanket/lib/timing"
)

const (
	// DialTimeout and ResponseHeaderTimeout are transport-level and
	// deliberately NOT scaled by timeMultiplier: they describe how long a
	// healthy local server may take to answer at all, which does not change
	// when a test compresses its own timing. Only the retry budget scales.
	DialTimeout           = 2 * time.Second
	ResponseHeaderTimeout = 5 * time.Second

	// DefaultRequestTimeout bounds a single attempt end to end (headers
	// plus body). Scaled by timeMultiplier.
	DefaultRequestTimeout = 15 * time.Second

	// DefaultBaseDelay and DefaultMaxDelay bound the full-jitter backoff.
	// Scaled by timeMultiplier.
	DefaultBaseDelay = 250 * time.Millisecond
	DefaultMaxDelay  = 10 * time.Second

	// DefaultDeadline is the total retry budget when a Policy doesn't set
	// one. Scaled by timeMultiplier.
	DefaultDeadline = 30 * time.Second

	// maxBodyBytes caps how much of a response body is read into memory.
	// Every response this package handles is a small JSON document; the cap
	// is here so a misbehaving/wrong endpoint can't balloon a worker's
	// memory.
	maxBodyBytes = 1 << 20
)

// sharedClient is the process-wide client. A single client (and therefore a
// single Transport) is what makes connection reuse work; constructing one
// per call, as the pre-phase-1 code effectively did via http.DefaultClient
// plus ad-hoc requests, also meant every call site silently inherited "no
// timeouts at all".
//
// Note there is no http.Client.Timeout: that would also cap streaming
// responses. Bounding a whole request is the caller's job, via a context
// deadline (see doOnce).
var sharedClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: ResponseHeaderTimeout,
		TLSHandshakeTimeout:   DialTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
	},
}

// Client returns the shared client, for the handful of callers that need to
// drive an http.Request themselves (streaming responses, say) rather than
// going through Do/DoOnce.
func Client() *http.Client {
	return sharedClient
}

// Result is a successful (2xx) response.
type Result struct {
	StatusCode int
	Body       []byte
}

// StatusError is a non-2xx response. It carries the status code so callers
// can branch on it (e.g. treat 404 on a task claim as "already gone" rather
// than an error) and so the retry loop can classify it.
type StatusError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e *StatusError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512] + "…"
	}
	return fmt.Sprintf("%s %s: unexpected status %s: %s", e.Method, e.URL, e.Status, body)
}

// Retryable reports whether retrying this request could plausibly succeed.
// 5xx is the server failing at something it might do successfully a moment
// later (including "restarting right now"); 429 is an explicit "come back".
// Everything else in the 4xx range is a statement about the request itself:
// 400 (malformed), 404 (gone), and — the one that matters most here — 409
// (this task is being run by somebody else, per the RunId fencing token).
// Retrying any of those just burns the deadline.
func (e *StatusError) Retryable() bool {
	return e.StatusCode >= 500 || e.StatusCode == http.StatusTooManyRequests
}

// TransportError is a failure to get any HTTP response at all: connection
// refused, connection reset, dial or response-header timeout, a truncated
// body. Always retryable — this is exactly the shape a server restart takes
// from a worker's point of view.
type TransportError struct {
	Method string
	URL    string
	Err    error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("%s %s: %v", e.Method, e.URL, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// IsRetryable classifies an error returned by DoOnce or Do.
func IsRetryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Retryable()
	}
	var te *TransportError
	return errors.As(err, &te)
}

// StatusCodeOf returns the HTTP status carried by err, or 0 if err isn't a
// StatusError.
func StatusCodeOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode
	}
	return 0
}

// Policy describes a retry budget. All four durations are unscaled
// constants; timeMultiplier is applied when they're used.
type Policy struct {
	// Deadline is the total wall-clock budget across all attempts.
	Deadline time.Duration
	// RequestTimeout bounds a single attempt.
	RequestTimeout time.Duration
	// BaseDelay and MaxDelay bound the full-jitter backoff:
	// rand(0, min(BaseDelay<<attempt, MaxDelay)).
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func (p Policy) withDefaults() Policy {
	if p.Deadline <= 0 {
		p.Deadline = DefaultDeadline
	}
	if p.RequestTimeout <= 0 {
		p.RequestTimeout = DefaultRequestTimeout
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = DefaultBaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = DefaultMaxDelay
	}
	return p
}

// FullJitter returns the backoff delay for a zero-based attempt number:
// a uniform random duration in [0, min(base<<attempt, max)].
//
// Full jitter rather than equal jitter or plain exponential: the failure
// mode this is defending against is every worker on a machine losing the
// server at the same instant, so the goal is to spread retries out, not
// merely to slow them down.
func FullJitter(base, max time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	if max < base {
		max = base
	}
	d := base
	for i := 0; i < attempt; i++ {
		if d >= max/2 {
			d = max
			break
		}
		d *= 2
	}
	if d > max {
		d = max
	}
	return time.Duration(rand.Int63n(int64(d) + 1))
}

// DoOnce performs a single request with the shared client's timeouts and a
// per-request context deadline, and classifies the response. A non-2xx
// status is returned as a *StatusError; a transport failure as a
// *TransportError. body may be nil.
func DoOnce(ctx context.Context, method, url string, body []byte, timeout time.Duration) (*Result, error) {
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timing.Scale(timeout))
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(rctx, method, url, rdr)
	if err != nil {
		// A malformed method/URL is a programming error, not a transient
		// failure: returned bare so IsRetryable says no.
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := sharedClient.Do(req)
	if err != nil {
		return nil, &TransportError{Method: method, URL: url, Err: err}
	}
	defer res.Body.Close()

	bts, readErr := io.ReadAll(io.LimitReader(res.Body, maxBodyBytes))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &StatusError{
			Method:     method,
			URL:        url,
			StatusCode: res.StatusCode,
			Status:     res.Status,
			Body:       string(bts),
		}
	}
	if readErr != nil {
		// Headers said 2xx but the body was truncated — treat as transport
		// failure so it can be retried.
		return nil, &TransportError{Method: method, URL: url, Err: readErr}
	}
	return &Result{StatusCode: res.StatusCode, Body: bts}, nil
}

// Do performs a request, retrying transient failures and 5xx responses with
// full-jitter backoff until p.Deadline elapses. Non-retryable responses
// (400/404/409, a bad URL) return immediately.
//
// The returned error on exhaustion wraps the last failure, so callers can
// still inspect it with errors.As.
func Do(ctx context.Context, method, url string, body []byte, p Policy) (*Result, error) {
	p = p.withDefaults()

	deadline := timing.Scale(p.Deadline)
	base := timing.Scale(p.BaseDelay)
	max := timing.Scale(p.MaxDelay)

	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var lastErr error
	for attempt := 0; ; attempt++ {
		res, err := DoOnce(ctx, method, url, body, p.RequestTimeout)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !IsRetryable(err) {
			return nil, err
		}

		timer := time.NewTimer(FullJitter(base, max, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("gave up after %d attempt(s) within %v: %w", attempt+1, deadline, lastErr)
		case <-timer.C:
		}
	}
}
