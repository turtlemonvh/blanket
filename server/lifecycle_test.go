// Tests for the standard-library server lifecycle (turtlemonvh/blanket#23
// phase 2): the ordered teardown, the shutdown-awareness of the four
// streaming routes, and the signal classification.
//
// The headline one is TestShutdown_CompletesWithAllStreamingRoutesOpen.
// The risk this phase carries is "Shutdown hangs on a streaming handler
// that was missed": net/http's Shutdown waits indefinitely on an active
// connection and never force-closes it, so one un-updated c.Stream call
// site would wedge every restart. There are exactly two call sites
// (sseStream in ui.go, streamLog in serve_logs.go) covering four routes,
// and this test holds all four open at once.
//
// What can't be tested in-process -- signals actually delivered to a real
// process, SIGUSR2 re-exec preserving the PID, exit codes -- lives in
// scripts/restart.sh on top of scripts/lib/harness.sh.

package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

// writeSeedLine puts one line in a log file so the tailer has something to
// deliver immediately. Without it the log-stream handlers write nothing
// until their idle timer fires, and the client's Do() -- which waits for
// response headers -- would block for that whole interval.
func writeSeedLine(t *testing.T, p string) {
	t.Helper()
	require.NoError(t, os.WriteFile(p, []byte("seed line\n"), 0644))
}

// streamReader consumes an SSE response body to EOF in the background.
type streamReader struct {
	route string
	resp  *http.Response
	done  chan struct{}

	mu   sync.Mutex
	body strings.Builder
}

func (sr *streamReader) read() {
	defer close(sr.done)
	defer sr.resp.Body.Close()
	buf := make([]byte, 4096)
	for {
		n, err := sr.resp.Body.Read(buf)
		if n > 0 {
			sr.mu.Lock()
			sr.body.Write(buf[:n])
			sr.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (sr *streamReader) text() string {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.body.String()
}

// TestShutdown_CompletesWithAllStreamingRoutesOpen is the mitigation the
// design names for this phase's main risk. All four streaming routes are
// open when Shutdown is called; it must return promptly (not after the
// deadline, and certainly not never), and every client must have received
// the server-restarting event rather than a bare disconnect.
func TestShutdown_CompletesWithAllStreamingRoutesOpen(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	// The log-stream handlers wait TimeMultiplier * LOGLINE_WAIT_DURATION
	// seconds per step; 1 gives the test a comfortable 5s window to get
	// all four streams established and shut down inside.
	s.TimeMultiplier = 1

	resultDir := t.TempDir()
	s.ResultsPath = resultDir
	taskLog := filepath.Join(resultDir, "blanket.stdout.log")
	writeSeedLine(t, taskLog)

	task := tasks.Task{
		Id:        objectid.NewObjectId(),
		State:     "RUNNING",
		ResultDir: resultDir,
	}
	require.NoError(t, s.DB.SaveTask(&task))

	workerLog := filepath.Join(t.TempDir(), "worker.log")
	writeSeedLine(t, workerLog)
	w := worker.WorkerConf{Id: objectid.NewObjectId(), Logfile: workerLog}
	require.NoError(t, s.DB.UpdateWorker(&w))

	bs, base := startTestListener(t, s)

	routes := []string{
		"/ui/sse/tasks",
		"/ui/sse/workers",
		"/task/" + task.Id.Hex() + "/log",
		"/worker/" + w.Id.Hex() + "/log",
	}

	client := &http.Client{}
	readers := make([]*streamReader, 0, len(routes))
	for _, route := range routes {
		req, err := http.NewRequest("GET", base+route, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err, "opening %s", route)
		require.Equal(t, http.StatusOK, resp.StatusCode, "opening %s", route)

		sr := &streamReader{route: route, resp: resp, done: make(chan struct{})}
		go sr.read()
		readers = append(readers, sr)
	}

	// All four handlers are inside their select by now (headers arrived).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	assert.NoError(t, bs.Shutdown(ctx), "Shutdown should drain, not hit its deadline")
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Second, "Shutdown took %s -- a streaming handler is not shutdown-aware", elapsed)

	for _, sr := range readers {
		select {
		case <-sr.done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: stream did not close after shutdown", sr.route)
		}
		body := sr.text()
		assert.Contains(t, body, "event:"+SSEServerRestartingEvent,
			"%s: client should be told the server is restarting, got %q", sr.route, body)
		assert.Contains(t, body, "retry:",
			"%s: client should get a reconnect hint, got %q", sr.route, body)
	}
}

// startTestListener runs a BlanketServer on an ephemeral loopback port and
// returns it with its base URL. The server is driven through the real
// serveListener path (signal subscription and all), so the test exercises
// what production runs rather than a stripped-down copy.
func startTestListener(t *testing.T, s *ServerConfig) (*BlanketServer, string) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	bs := s.Serve()
	// The database handle belongs to the test's own cleanup func.
	bs.closeStorage = nil

	served := make(chan error, 1)
	go func() { served <- bs.serveListener(ln) }()

	t.Cleanup(func() {
		bs.Shutdown(context.Background())
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("serveListener did not return after shutdown")
		}
	})

	return bs, "http://" + ln.Addr().String()
}

// The teardown steps must run in the documented order. In particular
// tailed_file.StopAll() must come *after* the listener is closed and
// handlers have returned -- doing it first is the graceful.v1
// BeforeShutdown bug this phase replaces, which left in-flight log-stream
// handlers blocked on a torn-down tailer.
func TestShutdown_TeardownOrder(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	var order []string
	record := func(step string) { order = append(order, step) }

	// Step (a) isn't a hook -- it's the channel every streaming handler
	// selects on -- so it's observed by checking the channel is already
	// closed by the time the later steps run, rather than by racing a
	// goroutine that has to be scheduled first.
	streamsSignalled := func() bool {
		select {
		case <-s.shutdownChan():
			return true
		default:
			return false
		}
	}

	bs := s.Serve()
	bs.stopTailers = func() {
		assert.True(t, streamsSignalled(),
			"streaming handlers must be released before the tailers they read from stop")
		record("tailers")
	}
	bs.stopLoops = func() { record("loops") }
	bs.closeStorage = func() { record("storage") }

	assert.False(t, streamsSignalled(), "nothing should be signalled before Shutdown")
	require.NoError(t, bs.Shutdown(context.Background()))

	assert.Equal(t, []string{"tailers", "loops", "storage"}, order)
	assert.True(t, streamsSignalled())
}

// Shutdown is reachable from both the signal path and the listener-error
// path, and ListenAndServe's own deferred cleanup, so it has to be safe to
// call repeatedly.
func TestShutdown_Idempotent(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	calls := 0
	bs := s.Serve()
	bs.stopTailers = func() {}
	bs.stopLoops = func() { calls++ }
	bs.closeStorage = func() {}

	require.NoError(t, bs.Shutdown(context.Background()))
	require.NoError(t, bs.Shutdown(context.Background()))
	require.NoError(t, bs.Shutdown(context.Background()))
	assert.Equal(t, 1, calls, "teardown should run exactly once")
}

// SIGTERM and SIGINT mean "stop"; only the platform restart signal means
// "restart". handleSignal is the seam that makes this testable without
// signalling the test binary itself.
func TestHandleSignal_StopSignalsDoNotRestart(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(sig.String(), func(t *testing.T) {
			s, cleanup := NewTestServer()
			defer cleanup()

			bs := s.Serve()
			bs.stopTailers = func() {}
			bs.closeStorage = func() {}

			assert.False(t, isRestartSignal(sig))
			// Returns rather than re-execing: a plain `systemctl stop`
			// must not bring anything back.
			assert.NoError(t, bs.handleSignal(sig))
		})
	}
}

// The signal set is platform-dependent: SIGUSR2 exists on unix only, and
// restart_windows.go leaves restartSignal nil so nothing subscribes it.
func TestShutdownSignals(t *testing.T) {
	sigs := shutdownSignals()
	assert.Contains(t, sigs, os.Signal(syscall.SIGTERM))
	assert.Contains(t, sigs, os.Signal(os.Interrupt))

	if restartSignal != nil {
		assert.Contains(t, sigs, restartSignal)
		assert.True(t, isRestartSignal(restartSignal))
	} else {
		assert.Len(t, sigs, 2)
	}
}

// The timeouts matter enough to pin down: a nonzero WriteTimeout is an
// absolute deadline on the whole response, which would kill every SSE
// stream in the app after that long.
func TestServerTimeouts(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	bs := s.Serve()
	defer bs.Shutdown(context.Background())

	assert.Zero(t, bs.http.WriteTimeout, "WriteTimeout must stay zero or SSE streams get cut")
	assert.Zero(t, bs.http.ReadTimeout, "ReadTimeout must stay zero: POST /task/ accepts large uploads")
	assert.Equal(t, DefaultReadHeaderTimeout, bs.http.ReadHeaderTimeout)
	assert.Equal(t, DefaultIdleTimeout, bs.http.IdleTimeout)
}

// The banner is the only thing that tells a user their page has stopped
// updating on purpose, so both halves -- the markup in the layout and the
// script that raises it -- have to actually be there and actually be
// served. Easy to drop one and not notice until a restart looks like a
// hang.
func TestUI_Layout_LoadsRestartBanner(t *testing.T) {
	cleanup := setupTestTaskType(t)
	defer cleanup()

	s, scleanup := NewTestServer()
	defer scleanup()
	r := s.GetRouter()

	for _, path := range []string{"/ui/", "/ui/workers", "/ui/task-types", "/ui/about"} {
		w := getUI(r, path)
		assert.Equal(t, http.StatusOK, w.Code, path)
		body := w.Body.String()
		assert.Contains(t, body, `src="/ui/static/sse-restart-banner.js"`, path)
		assert.Contains(t, body, `id="server-restart-banner"`, path)
		// Hidden until a stream reports a restart.
		assert.Contains(t, body, `id="server-restart-banner" class="flash-area" hidden`, path)
	}

	asset := getUI(r, "/ui/static/sse-restart-banner.js")
	assert.Equal(t, http.StatusOK, asset.Code)
	js := asset.Body.String()
	assert.Contains(t, js, "htmx:sseOpen")
	assert.Contains(t, js, SSEServerRestartingEvent)
	assert.Contains(t, js, "server-restart-banner")
}

// Guard against a regression where a handler writes the restart frame
// without the reconnect hint (or writes it in the wrong order): the
// `retry:` field has to land before the event, or a browser that
// disconnects on the event uses its default backoff.
func TestWriteServerRestarting_Format(t *testing.T) {
	var buf strings.Builder
	writeServerRestarting(io.Writer(&buf))

	out := buf.String()
	retryAt := strings.Index(out, "retry:")
	eventAt := strings.Index(out, "event:"+SSEServerRestartingEvent)
	require.NotEqual(t, -1, retryAt, "missing retry hint: %q", out)
	require.NotEqual(t, -1, eventAt, "missing server-restarting event: %q", out)
	assert.Less(t, retryAt, eventAt, "retry hint must precede the event: %q", out)
}
