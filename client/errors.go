package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/turtlemonvh/blanket/lib/httpx"
)

// maxAPIErrorMessageLen bounds how much of a non-JSON error body ends up in
// an APIError's Message, so a caller (and whatever terminal it's printed
// to) isn't at the mercy of an unbounded response body.
const maxAPIErrorMessageLen = 512

// APIError is returned by every client call when the blanket server
// answers with a non-2xx status. Before this type existed, a non-2xx body
// was unmarshaled as if it were a successful response -- decoding an
// error body into a zero-value struct -- so e.g. `blanket submit` on a
// 400 printed an all-zero task id and exited 0
// (turtlemonvh/blanket#112).
type APIError struct {
	// Status is the HTTP status code the server returned.
	Status int
	// Message is the server's error text: the "error" field of blanket's
	// usual {"error": "..."} JSON body, or -- for the handful of
	// handlers that answer with plain text -- the response body itself,
	// trimmed and truncated to maxAPIErrorMessageLen.
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("server returned %d: %s", e.Status, e.Message)
}

// newAPIError builds an APIError from a non-2xx status and its raw
// response body.
func newAPIError(status int, body []byte) *APIError {
	return &APIError{Status: status, Message: parseErrorMessage(body)}
}

// parseErrorMessage decodes blanket's usual {"error": "..."} error body,
// falling back to the raw body (trimmed and truncated) for handlers that
// answer with plain text instead -- which also covers a body that is
// neither shape (malformed JSON, empty, binary garbage): it just becomes
// the fallback text.
func parseErrorMessage(body []byte) string {
	var withError struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &withError); err == nil && withError.Error != "" {
		return withError.Error
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "(empty response body)"
	}
	if len(trimmed) > maxAPIErrorMessageLen {
		trimmed = trimmed[:maxAPIErrorMessageLen] + "…"
	}
	return trimmed
}

// doRequest is the single place every client call goes through to talk to
// the server: it performs one request via lib/httpx's shared client and
// classifies the response before any caller gets a chance to decode it.
// A non-2xx response comes back as an *APIError carrying the decoded
// server message rather than a body for the caller to unmarshal as if it
// were a success. A transport failure (connection refused, timeout, a
// truncated body) passes through unchanged as *httpx.TransportError.
func doRequest(ctx context.Context, method, url string, body []byte, timeout time.Duration) (*httpx.Result, error) {
	res, err := httpx.DoOnce(ctx, method, url, body, timeout)
	if err != nil {
		var se *httpx.StatusError
		if errors.As(err, &se) {
			return nil, newAPIError(se.StatusCode, []byte(se.Body))
		}
		return nil, err
	}
	return res, nil
}
