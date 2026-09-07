package command

/*

Tests for the part of `blanket upgrade` / `blanket rollback` that decides a
restart worked (turtlemonvh/blanket#23 phase 6, turtlemonvh/blanket#87).

They exist because of a specific bug. `waitForNewServer` used to accept the
first server that answered with an instance id it had not seen, and there
is a third process that satisfies that: a server the CLI started against
one that turned out to be re-execing in place, which then sat on the
database lock and won the port at the *next* restart. It answered as the
version that was supposed to have been replaced, with an instance id of its
own, and so "verified" a rollback that had not happened.

So the fake server here is a state machine, not a fixture: it answers as
one thing and then as another, and the test's claim is about which of them
the wait accepts.

*/

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOps is a stand-in for GET /ops/restart/status whose answer can
// change between calls. Answers are handed out in order; the last one
// repeats forever.
type fakeOps struct {
	mu      sync.Mutex
	answers []restartStatus
	calls   int
}

func (f *fakeOps) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	i := f.calls
	if i >= len(f.answers) {
		i = len(f.answers) - 1
	}
	f.calls++
	st := f.answers[i]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

func banner(v string) string { return fmt.Sprintf("blanket %s (built test)", v) }

func status(instanceId, version string) restartStatus {
	st := restartStatus{InstanceId: instanceId, Version: banner(version)}
	st.Restart.State = "IDLE"
	return st
}

// startFakeOps serves answers on a loopback port and returns that port.
func startFakeOps(t *testing.T, answers ...restartStatus) (*fakeOps, int) {
	t.Helper()
	f := &fakeOps{answers: answers}
	mux := http.NewServeMux()
	mux.Handle("/ops/restart/status", f)
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, portOf(t, srv.URL)
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	p, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return p
}

// freePort returns a loopback port with nothing listening on it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// ---------------------------------------------------------------------------
// waitForNewServer
// ---------------------------------------------------------------------------

// The regression this file exists for: something answers on the port that
// is neither the process being replaced nor the replacement -- a stray
// server on the OLD version, with an instance id of its own. The wait must
// not take it for the replacement; it must keep waiting, and take the
// v9.9.0 server that shows up afterwards.
func TestWaitForNewServer_WaitsOutAnOldVersionImpostor(t *testing.T) {
	answers := []restartStatus{}
	for i := 0; i < 3; i++ {
		answers = append(answers, status("stray-process", "v9.9.1"))
	}
	answers = append(answers, status("the-replacement", "v9.9.0"))
	_, port := startFakeOps(t, answers...)

	st, err := waitForNewServer(port, "the-old-process", "v9.9.0", 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "the-replacement", st.InstanceId,
		"a different instance id is not on its own evidence of a restart; the version has to match too")
}

// And when nothing but the impostor ever answers, the caller has to be
// able to say *which* version came back -- that is the difference between
// "the server never came up" and "something else is on this port".
func TestWaitForNewServer_ReportsTheVersionThatAnsweredWhenTheBudgetRunsOut(t *testing.T) {
	_, port := startFakeOps(t, status("stray-process", "v9.9.1"))

	st, err := waitForNewServer(port, "the-old-process", "v9.9.0", 600*time.Millisecond)
	assert.Nil(t, st)
	require.Error(t, err)

	var wrong *wrongVersionError
	require.ErrorAs(t, err, &wrong)
	assert.Equal(t, "v9.9.1", wrong.Got)
	assert.Equal(t, "v9.9.0", wrong.Want)
}

// The original check, still enforced: the process being replaced keeps
// answering right up until its listener closes.
func TestWaitForNewServer_RefusesTheProcessBeingReplaced(t *testing.T) {
	_, port := startFakeOps(t, status("the-old-process", "v9.9.0"))

	st, err := waitForNewServer(port, "the-old-process", "v9.9.0", 600*time.Millisecond)
	assert.Nil(t, st)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no replacement server answered")
}

// When the instance id of the server being replaced was never captured --
// it was between processes when the CLI looked -- the id proves nothing
// and the version is the only evidence there is. It must still be checked.
func TestWaitForNewServer_WithNoFromInstanceIdStillChecksTheVersion(t *testing.T) {
	answers := []restartStatus{
		status("some-process", "v9.9.1"),
		status("some-process", "v9.9.1"),
		status("the-replacement", "v9.9.0"),
	}
	_, port := startFakeOps(t, answers...)

	st, err := waitForNewServer(port, "", "v9.9.0", 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "the-replacement", st.InstanceId)
}

// A development build records no version, and the server's banner may name
// none either. Neither side can prove anything, so the version check is
// skipped rather than failed -- otherwise no dev build could ever verify.
func TestWaitForNewServer_UnknownVersionIsNotAMismatch(t *testing.T) {
	_, port := startFakeOps(t, status("the-replacement", "v9.9.0"))

	st, err := waitForNewServer(port, "the-old-process", "", 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "the-replacement", st.InstanceId)
}

// A server that will not say who it is is not a verification either.
func TestWaitForNewServer_RefusesAnEmptyInstanceId(t *testing.T) {
	_, port := startFakeOps(t, status("", "v9.9.0"))

	_, err := waitForNewServer(port, "the-old-process", "v9.9.0", 600*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no replacement server answered")
}

// ---------------------------------------------------------------------------
// awaitRestartStatus
// ---------------------------------------------------------------------------

// "Nothing answered at this instant" and "there is no server here" are
// different claims, and the CLI acts on them in opposite ways. A server
// re-execing in place is not listening for a moment; asking once reads
// that as no server at all.
func TestAwaitRestartStatus_RetriesWhileTheServerIsBetweenProcesses(t *testing.T) {
	port := freePort(t)

	// Bring a server up on that port shortly after the wait starts, the
	// way a re-exec does.
	up := make(chan *httptest.Server, 1)
	go func() {
		time.Sleep(700 * time.Millisecond)
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			close(up)
			return
		}
		mux := http.NewServeMux()
		mux.Handle("/ops/restart/status", &fakeOps{answers: []restartStatus{status("the-replacement", "v9.9.0")}})
		srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: mux}}
		srv.Start()
		up <- srv
	}()
	t.Cleanup(func() {
		if srv := <-up; srv != nil {
			srv.Close()
		}
	})

	st, err := awaitRestartStatus(port, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "the-replacement", st.InstanceId)
}

// It still gives up, though: a port nothing is on must report a server
// that is down, not hang until some outer timeout notices.
func TestAwaitRestartStatus_GivesUpOnAPortNothingIsOn(t *testing.T) {
	port := freePort(t)

	start := time.Now()
	_, err := awaitRestartStatus(port, 500*time.Millisecond)
	require.Error(t, err)
	assert.ErrorIs(t, err, errServerDown)
	assert.Less(t, time.Since(start), 10*time.Second, "the retry has to be bounded")
}

// An HTTP answer is a real answer. A 403 from the ops guard means "do not
// go behind my back", and asking four more times only delays saying so.
func TestAwaitRestartStatus_DoesNotRetryAServerThatAnswered(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/ops/restart/status", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, err := awaitRestartStatus(portOf(t, srv.URL), 5*time.Second)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errServerDown)
	assert.Equal(t, 1, calls, "a server that answered was asked exactly once")
}

// ---------------------------------------------------------------------------
// portStaysQuiet
// ---------------------------------------------------------------------------

// The other half of the same distinction, read from the other side: the
// CLI only starts a server of its own when the port is quiet and stays
// quiet. A server that is merely between process images must not look like
// one that has exited.
func TestPortStaysQuiet(t *testing.T) {
	assert.True(t, portStaysQuiet(freePort(t), 600*time.Millisecond),
		"nothing is on this port")

	_, port := startFakeOps(t, status("someone", "v9.9.0"))
	assert.False(t, portStaysQuiet(port, 5*time.Second),
		"something is answering, so the CLI must not start a second server")
}
