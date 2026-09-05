package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFullJitter_Bounds pins the two properties the retry story depends on:
// every delay lands in [0, min(base<<attempt, max)] — so a caller can bound
// its own deadline — and the ceiling actually grows with the attempt number
// until it hits max, rather than staying flat.
//
// It also checks the delays are genuinely spread rather than clustered:
// full jitter exists specifically so that a machine's workers, which all
// lose the server at the same instant, don't retry in lockstep.
func TestFullJitter_Bounds(t *testing.T) {
	base := 10 * time.Millisecond
	max := 160 * time.Millisecond

	for attempt := 0; attempt < 8; attempt++ {
		ceiling := base << attempt
		if ceiling > max || ceiling <= 0 {
			ceiling = max
		}
		for i := 0; i < 200; i++ {
			d := FullJitter(base, max, attempt)
			assert.GreaterOrEqual(t, d, time.Duration(0))
			assert.LessOrEqual(t, d, ceiling, "attempt %d exceeded its ceiling", attempt)
		}
	}

	// Spread: with 200 samples at a 160ms ceiling, hitting only one distinct
	// value would mean the jitter isn't jittering.
	seen := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		seen[FullJitter(base, max, 4)] = true
	}
	assert.Greater(t, len(seen), 10, "full jitter produced almost no variation")

	// Degenerate inputs stay sane rather than panicking on rand.Int63n(0).
	assert.Equal(t, time.Duration(0), FullJitter(0, max, 3))
	assert.LessOrEqual(t, FullJitter(max, base, 3), max, "a max below base is clamped up to base")
}

func TestStatusError_Classification(t *testing.T) {
	cases := []struct {
		code  int
		retry bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusTooManyRequests, true},
		{http.StatusConflict, false},
		{http.StatusNotFound, false},
		{http.StatusBadRequest, false},
	}
	for _, c := range cases {
		err := &StatusError{StatusCode: c.code}
		assert.Equal(t, c.retry, err.Retryable(), "status %d", c.code)
		assert.Equal(t, c.retry, IsRetryable(err), "status %d via IsRetryable", c.code)
		assert.Equal(t, c.code, StatusCodeOf(err))
	}

	assert.True(t, IsRetryable(&TransportError{Err: errors.New("connection reset")}))
	assert.False(t, IsRetryable(errors.New("some other problem")))
	assert.Equal(t, 0, StatusCodeOf(errors.New("some other problem")))
}

// TestDo_RetriesServerErrorsThenSucceeds is the shape of a server restart:
// a few failures, then the same request works.
func TestDo_RetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok": true}`))
	}))
	defer srv.Close()

	res, err := Do(context.Background(), "PUT", srv.URL, nil, Policy{
		Deadline:  5 * time.Second,
		BaseDelay: time.Millisecond,
		MaxDelay:  5 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, `{"ok": true}`, string(res.Body))
	assert.Equal(t, int64(4), calls.Load())
}

// TestDo_DoesNotRetryClientErrors: 409 in particular. The worker's fencing
// token turns "another run owns this task" into a 409, and retrying it
// would burn a five-minute deadline achieving nothing.
func TestDo_DoesNotRetryClientErrors(t *testing.T) {
	for _, code := range []int{http.StatusConflict, http.StatusNotFound, http.StatusBadRequest} {
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
			w.Write([]byte(`{"error": "nope"}`))
		}))

		_, err := Do(context.Background(), "PUT", srv.URL, nil, Policy{
			Deadline:  5 * time.Second,
			BaseDelay: time.Millisecond,
		})
		srv.Close()

		require.Error(t, err)
		assert.Equal(t, code, StatusCodeOf(err))
		assert.Equal(t, int64(1), calls.Load(), "status %d should not have been retried", code)
	}
}

// TestDo_GivesUpAtDeadline: a server that never recovers must return an
// error within the budget rather than blocking forever, and the error must
// still carry the last failure so callers can inspect it.
func TestDo_GivesUpAtDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	started := time.Now()
	_, err := Do(context.Background(), "PUT", srv.URL, nil, Policy{
		Deadline:  200 * time.Millisecond,
		BaseDelay: 5 * time.Millisecond,
		MaxDelay:  20 * time.Millisecond,
	})
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, StatusCodeOf(err),
		"the wrapped last failure should still be inspectable")
	assert.Less(t, elapsed, 5*time.Second)
	assert.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
}

// TestDo_RetriesTransportErrors: a closed listener is the "server is gone"
// case, and it must be retried rather than reported immediately.
func TestDo_RetriesTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	_, err := Do(context.Background(), "PUT", url, nil, Policy{
		Deadline:  150 * time.Millisecond,
		BaseDelay: 5 * time.Millisecond,
		MaxDelay:  20 * time.Millisecond,
	})
	require.Error(t, err)

	var te *TransportError
	assert.True(t, errors.As(err, &te), "expected a transport error, got %v", err)
}

// TestDoOnce_PerRequestTimeout: a handler that never answers must not hang
// the caller. This is the defect the shared client exists for — every
// worker→server call used http.DefaultClient, which has no timeouts at all.
func TestDoOnce_PerRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	started := time.Now()
	_, err := DoOnce(context.Background(), "GET", srv.URL, nil, 100*time.Millisecond)
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.True(t, IsRetryable(err), "a wedged server should look transient, not fatal")
	assert.Less(t, elapsed, 5*time.Second)
}

// TestDoOnce_NoContentIsSuccess: 204 is how the claim endpoint says "empty
// queue", and must not be classified as an error.
func TestDoOnce_NoContentIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res, err := DoOnce(context.Background(), "POST", srv.URL, nil, time.Second)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, res.StatusCode)
	assert.Empty(t, res.Body)
}
