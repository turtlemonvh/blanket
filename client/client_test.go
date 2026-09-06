// Tests for client.go's one-shot calls against a real HTTP server: the
// bug this guards against (turtlemonvh/blanket#112) is a non-2xx
// response decoding as if it were a successful one, which only shows up
// once something is actually listening and answering with an error body.

package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPort starts an httptest server with handler and returns the port to
// pass to a client.* function (which all build "http://localhost:<port>/...").
func testPort(t *testing.T, handler http.HandlerFunc) (int, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return port, srv.Close
}

func assertAPIError(t *testing.T, err error, wantStatus int, wantMessageContains string) {
	t.Helper()
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr, "expected an *APIError, got %T: %v", err, err)
	assert.Equal(t, wantStatus, apiErr.Status)
	assert.Contains(t, apiErr.Message, wantMessageContains)
}

func TestSubmitTaskWithOptions_JSONErrorBody(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": "unknown task type 'nope'"}`)
	})
	defer closeSrv()

	_, err := SubmitTask("nope", nil, port)
	assertAPIError(t, err, http.StatusBadRequest, "unknown task type 'nope'")
}

func TestSubmitTaskWithOptions_PlainTextErrorBody(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal server error: db is wedged")
	})
	defer closeSrv()

	_, err := SubmitTask("echo_task", nil, port)
	assertAPIError(t, err, http.StatusInternalServerError, "internal server error: db is wedged")
}

func TestSubmitTaskWithOptions_Success(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id": "68b4c1f2a3e4d5b6c7a8f9e0", "type": "echo_task"}`)
	})
	defer closeSrv()

	tsk, err := SubmitTask("echo_task", nil, port)
	require.NoError(t, err)
	assert.Equal(t, "echo_task", tsk.TypeId)
	assert.False(t, tsk.Id.IsZero(), "a successful submit must decode the real id, not a zero value")
}

func TestGetTasks_NotFound(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error": "no such route"}`)
	})
	defer closeSrv()

	_, err := GetTasks(&GetTasksConf{Limit: 10}, port)
	assertAPIError(t, err, http.StatusNotFound, "no such route")
}

func TestGetTasks_Success(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[{"id": "68b4c1f2a3e4d5b6c7a8f9e0", "type": "echo_task"}]`)
	})
	defer closeSrv()

	tsks, err := GetTasks(&GetTasksConf{Limit: 10}, port)
	require.NoError(t, err)
	require.Len(t, tsks, 1)
	assert.Equal(t, "echo_task", tsks[0]["type"])
}

func TestGetActiveWorkerTagSets_JSONErrorBody(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": "bad query"}`)
	})
	defer closeSrv()

	_, err := GetActiveWorkerTagSets(port)
	assertAPIError(t, err, http.StatusBadRequest, "bad query")
}

func TestGetActiveWorkerTagSets_Success(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[{"tags": ["os:unix"], "stopped": false}, {"tags": ["os:windows"], "stopped": true}]`)
	})
	defer closeSrv()

	sets, err := GetActiveWorkerTagSets(port)
	require.NoError(t, err)
	require.Len(t, sets, 1, "a stopped worker's tag set must be excluded")
	assert.Equal(t, []string{"os:unix"}, sets[0])
}

func TestDeleteTask_PlainTextErrorBody(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "'nope' is not not a valid objectid")
	})
	defer closeSrv()

	err := DeleteTask("nope", port)
	assertAPIError(t, err, http.StatusInternalServerError, "not a valid objectid")
}

func TestDeleteTask_Success(t *testing.T) {
	port, closeSrv := testPort(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id": "68b4c1f2a3e4d5b6c7a8f9e0"}`)
	})
	defer closeSrv()

	err := DeleteTask("68b4c1f2a3e4d5b6c7a8f9e0", port)
	require.NoError(t, err)
}

func TestParseErrorMessage_TruncatesLongBody(t *testing.T) {
	long := make([]byte, maxAPIErrorMessageLen+100)
	for i := range long {
		long[i] = 'x'
	}
	msg := parseErrorMessage(long)
	assert.LessOrEqual(t, len(msg), maxAPIErrorMessageLen+len("…"))
	assert.Contains(t, msg, "…")
}

func TestParseErrorMessage_EmptyBody(t *testing.T) {
	assert.Equal(t, "(empty response body)", parseErrorMessage(nil))
	assert.Equal(t, "(empty response body)", parseErrorMessage([]byte("   ")))
}
