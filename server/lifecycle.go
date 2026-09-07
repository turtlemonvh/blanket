package server

/*

Server lifecycle: startup, signal handling, and ordered shutdown
(turtlemonvh/blanket#23 phase 2).

This replaces gopkg.in/tylerb/graceful.v1, which is archived upstream,
predates net/http's own Server.Shutdown, and collapses shutdown into a
single signal-driven phase. Two things made that a problem:

  - Its BeforeShutdown hook ran *before* the listener closed, and that is
    where tailed_file.StopAll() was called. In-flight log-stream handlers
    were left blocked on a tailer that had already been torn down. The
    ordering below fixes that: tailers stop only once no handler can
    still be reading from them.

  - It has no RegisterOnShutdown-style hook, which is exactly what SSE
    handlers need. net/http's Shutdown waits *indefinitely* for active
    connections and never force-closes them, so a streaming handler that
    doesn't notice the shutdown hangs it forever. Hence the server-wide
    shutdown channel (ServerConfig.shutdownChan) every c.Stream call site
    selects on -- see server/ui.go's sseStream and server/serve_logs.go's
    streamLog.

Teardown runs in this fixed order (BlanketServer.Shutdown):

	a. signal shutdown to streaming handlers (they emit `retry:` + a
	   `server-restarting` SSE event and return)
	b. http.Server.Shutdown(ctx) -- stop listening, drain in-flight work
	c. http.Server.Close() if (b) hits its deadline
	d. tailed_file.StopAll()
	e. cancel background loops (the scheduler; phase 3's reaper joins it
	   inside startBackgroundLoops, needing no new plumbing here)
	f. close storage (ServerConfig.Cleanup -- the BoltDB handle)

Signals (BlanketServer.ListenAndServe):

	SIGINT / SIGTERM  drain and exit 0, leaving no restart intent behind.
	                  A plain `systemctl stop` must not resurrect anything
	                  later. (There is no persisted respawn intent in the
	                  tree today; phase 5 adds one, and this exit path is
	                  where it must be cleared.)
	SIGUSR2 (unix)    drain, then re-exec this binary in place with the
	                  same argv and environment -- the PID is preserved.
	                  Windows has no SIGUSR2 and never self-restarts; see
	                  restart_windows.go.

Phase 5 adds a third way in, alongside the two signals:
POST /ops/restart/exec, which runs the same teardown and then does
whatever --exec-mode resolved to -- re-exec in place, or exit with
RestartExitCode for a supervisor to notice. SIGUSR2 stays unconditionally
the re-exec-in-place behaviour: it is what an operator sends when there is
no supervisor, and reinterpreting it would take away the only way to ask
for that specifically.

The other half of phase 5 in this file is the boot sequence. Serve adopts
whatever restart record the previous process left in the `meta` bucket
(server/restart.go), and once the listener is up, brings back the workers
that record's drain stopped -- after the listener, because a worker
registers itself over HTTP and there is nothing to register with before
then.

*/

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/tailed_file"
)

const (
	// DefaultShutdownTimeout is how long Shutdown gives in-flight requests
	// to finish after the listener closes, before remaining connections are
	// force-closed. Streaming handlers return as soon as they see the
	// shutdown signal, so this is a backstop for a wedged handler rather
	// than the normal path.
	DefaultShutdownTimeout = 5 * time.Second

	// DefaultReadHeaderTimeout bounds how long a client may take to send
	// its request headers. Guards against a slowloris-style stall holding a
	// connection (and, at shutdown, the drain) open indefinitely.
	DefaultReadHeaderTimeout = 10 * time.Second

	// DefaultIdleTimeout is how long an idle keep-alive connection is kept
	// around. Browsers open several per page; without this they linger.
	DefaultIdleTimeout = 120 * time.Second
)

// Note there is deliberately no ReadTimeout or WriteTimeout.
//
//   - WriteTimeout is an absolute deadline on the whole response, so any
//     nonzero value kills every SSE stream (/ui/sse/*, /task/:id/log,
//     /worker/:id/log) after that long. It must stay zero.
//   - ReadTimeout would likewise cap the whole request read, including the
//     body -- and POST /task/ accepts multipart uploads of arbitrary size.
//     ReadHeaderTimeout gives the slowloris protection without that cost.

// BlanketServer owns the running net/http server and the ordered teardown
// described at the top of this file. Build one with ServerConfig.Serve.
type BlanketServer struct {
	cfg  *ServerConfig
	http *http.Server

	// shutdownTimeout is the deadline handed to http.Server.Shutdown
	// before Close() force-closes what's left.
	shutdownTimeout time.Duration

	// restartCh carries a resolved exec mode from POST /ops/restart/exec
	// to the goroutine holding the listener. Buffered, and written
	// non-blockingly, so a second exec request while the first is already
	// in flight is dropped rather than queued -- there is only one
	// shutdown to perform.
	restartCh chan string

	// Teardown steps (d), (e) and (f), as fields so tests can substitute
	// fakes and assert the order they run in. stopLoops is set by Serve
	// from startBackgroundLoops; closeStorage from ServerConfig.Cleanup.
	stopTailers  func()
	stopLoops    func()
	closeStorage func()

	shutdownOnce sync.Once
	shutdownErr  error
}

// Serve builds the HTTP server and starts the background loops. It does
// not listen; call ListenAndServe on the result.
func (s *ServerConfig) Serve() *BlanketServer {
	log.WithFields(log.Fields{
		"port": s.Port,
	}).Info("Starting main server")

	// Record which process now owns this database, before anything else
	// can observe the server as up (turtlemonvh/blanket#23 phase 4).
	s.persistInstance()

	// Reconcile a restart record the previous process left behind, before
	// the listener opens: an operator who reaches GET /ops/restart/status
	// should never see a state this process has already decided is over.
	// turtlemonvh/blanket#23 phase 5.
	warnIfCrashInjectionArmed()
	s.adoptRestartRecordOnBoot()

	// Background loops: currently just the task scheduler (SCHEDULED /
	// RECURRING tasks; see server/scheduler.go). Also the place a future
	// reaper loop for cleaning the queue/db/workers (turtlemonvh/blanket#23
	// phase 3) should be added -- startBackgroundLoops is structured so
	// that's one more `go s.xLoop(ctx)` call, not new start/stop plumbing,
	// and the single stop function below already covers it.
	stopBackgroundLoops := s.startBackgroundLoops(context.Background())

	timeout := s.ShutdownTimeout
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}

	bs := &BlanketServer{
		cfg: s,
		http: &http.Server{
			Addr:              fmt.Sprintf(":%d", s.Port),
			Handler:           s.GetRouter(),
			ReadHeaderTimeout: DefaultReadHeaderTimeout,
			IdleTimeout:       DefaultIdleTimeout,
			// WriteTimeout stays zero on purpose -- see above.
		},
		shutdownTimeout: timeout,
		stopTailers:     tailed_file.StopAll,
		stopLoops:       stopBackgroundLoops,
		closeStorage:    s.Cleanup,
		restartCh:       make(chan string, 1),
	}

	// POST /ops/restart/exec needs a way to reach the goroutine that owns
	// the listener. A channel rather than calling Shutdown from the
	// handler: the handler *is* one of the requests the shutdown drains,
	// so it has to hand the work over and return.
	s.restartExecFn = bs.requestRestart

	return bs
}

// Addr reports the address the server is configured to listen on.
func (bs *BlanketServer) Addr() string { return bs.http.Addr }

// ListenAndServe binds the configured port and blocks until the server is
// asked to stop, then runs the teardown. On SIGUSR2 (unix only) it re-execs
// this binary in place after the teardown and therefore never returns.
//
// Returns nil for a clean shutdown, or the listener's error if it failed to
// start or died on its own.
func (bs *BlanketServer) ListenAndServe() error {
	ln, err := net.Listen("tcp", bs.http.Addr)
	if err != nil {
		// Nothing is listening, but the background loops and the database
		// handle are already live; tear them down so the caller doesn't
		// leave a bolt lock behind on a port conflict.
		bs.Shutdown(context.Background())
		return err
	}
	return bs.serveListener(ln)
}

// serveListener is ListenAndServe's body once a listener exists. Split out
// so tests can drive a real server on an ephemeral port.
func (bs *BlanketServer) serveListener(ln net.Listener) error {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, shutdownSignals()...)
	defer signal.Stop(sigCh)

	serveErr := make(chan error, 1)
	go func() {
		err := bs.http.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	// Bring back whatever the previous process's drain stopped. In a
	// goroutine because a respawned worker registers itself over HTTP
	// against the listener this function is about to start serving on.
	go bs.cfg.respawnWorkers("boot")

	select {
	case err := <-serveErr:
		// The listener died without anyone asking it to. Still run the
		// teardown so storage and the loops are released.
		bs.Shutdown(context.Background())
		return err
	case sig := <-sigCh:
		return bs.handleSignal(sig)
	case mode := <-bs.restartCh:
		return bs.handleRestart(mode)
	}
}

// requestRestart asks the serving goroutine to tear down and restart in
// `mode` (an already-resolved database.ExecMode* value). Returns
// immediately; the caller is a request handler that the shutdown it just
// asked for is about to drain.
func (bs *BlanketServer) requestRestart(mode string) {
	select {
	case bs.restartCh <- mode:
	default:
		log.WithField("execMode", mode).Warn("a restart is already in flight; ignoring the duplicate request")
	}
}

// handleRestart runs the teardown and then does what mode says.
//
// ExecModeExit returns an ExitCodeError rather than exiting here, so the
// exit stays in main's hands: a package that calls os.Exit from inside a
// library function is a package that cannot be tested and cannot be reused
// by a caller with its own cleanup to do.
func (bs *BlanketServer) handleRestart(mode string) error {
	log.WithField("execMode", mode).Warn("shutting down for a requested restart")

	ctx, cancel := context.WithTimeout(context.Background(), bs.shutdownTimeout)
	defer cancel()
	bs.Shutdown(ctx)

	if mode == database.ExecModeExec {
		// Storage is closed and the bolt lock released, so the new image
		// can take it. Does not return on success.
		if err := reexecSelf(); err != nil {
			// Windows always lands here (reexecSelf refuses), and so does
			// a unix box whose binary has been deleted out from under it.
			// Falling through to the exit path is the right answer for
			// both: a supervisor gets its chance, and an unsupervised
			// operator gets a clear exit code instead of a server that
			// silently kept running an image it was told to replace.
			log.WithField("err", err).Warn("could not re-exec in place; exiting for a supervisor or the upgrade CLI instead")
		}
	}

	return &ExitCodeError{
		Code: RestartExitCode,
		Msg:  "exiting so a supervisor (or the upgrade CLI) can start the replacement",
	}
}

// ExitCodeError asks main for a specific process exit code. See
// RestartExitCode for why a restart's exit must not be 0.
type ExitCodeError struct {
	Code int
	Msg  string
}

func (e *ExitCodeError) Error() string {
	return fmt.Sprintf("%s (exit %d)", e.Msg, e.Code)
}

// handleSignal runs the teardown for sig and then either returns (exit) or
// re-execs (restart). Exported behaviour lives here rather than in main so
// it is reachable from a test without actually signalling the test binary.
func (bs *BlanketServer) handleSignal(sig os.Signal) error {
	restart := isRestartSignal(sig)

	log.WithFields(log.Fields{
		"signal":  sig.String(),
		"restart": restart,
	}).Warn("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), bs.shutdownTimeout)
	defer cancel()
	bs.Shutdown(ctx)

	if !restart {
		// SIGINT/SIGTERM: exit leaving nothing behind that would bring the
		// server (or its workers) back. See the file header.
		return nil
	}

	// Storage is closed and the bolt lock released, so the new image can
	// take it. reexecSelf does not return on success.
	if err := reexecSelf(); err != nil {
		log.WithField("err", err).Error("self-restart failed; exiting instead")
		return err
	}
	return nil
}

// Shutdown runs the ordered teardown described at the top of this file.
// Safe to call more than once and from more than one goroutine; only the
// first call does any work. ctx bounds step (b) only.
func (bs *BlanketServer) Shutdown(ctx context.Context) error {
	bs.shutdownOnce.Do(func() { bs.shutdownErr = bs.shutdown(ctx) })
	return bs.shutdownErr
}

func (bs *BlanketServer) shutdown(ctx context.Context) error {
	// (a) Tell the streaming handlers to wrap up. Without this,
	// http.Server.Shutdown blocks forever on an open SSE connection: it
	// waits for active connections and never force-closes them.
	if bs.cfg != nil {
		bs.cfg.signalShutdown()
	}

	// (b) Stop accepting connections, drain what's in flight.
	err := bs.http.Shutdown(ctx)
	if err != nil {
		// (c) The deadline passed with connections still active. Cut them.
		log.WithField("err", err).Warn("graceful shutdown deadline passed; force-closing connections")
		if closeErr := bs.http.Close(); closeErr != nil {
			log.WithField("err", closeErr).Warn("force-close failed")
		}
	}

	// (d) Only now are the tailers safe to stop: no handler can still be
	// reading from one. This is the ordering bug graceful.v1's
	// BeforeShutdown hook had.
	if bs.stopTailers != nil {
		bs.stopTailers()
	}

	// (e) Background loops (scheduler today, + phase 3's reaper).
	if bs.stopLoops != nil {
		bs.stopLoops()
	}

	// (f) Storage last -- everything above may still touch the database.
	//
	// The lock-holder record goes first, and is the reason this step is
	// two statements rather than one: a record left behind after the
	// handle closes would tell the *next* process that a crashed blanket
	// still owns the file. Clearing it on the clean path is what makes
	// finding one there meaningful (turtlemonvh/blanket#23 phase 4).
	if bs.cfg != nil && bs.cfg.DB != nil {
		if err := bs.cfg.DB.ClearLockHolder(); err != nil {
			log.WithField("err", err).Warn("could not clear the lock-holder record at shutdown")
		}
	}
	if bs.closeStorage != nil {
		bs.closeStorage()
	}

	log.Info("shutdown complete")
	return err
}

// shutdownSignals is the set ListenAndServe subscribes to: always SIGINT
// and SIGTERM, plus the platform's restart signal where it has one.
func shutdownSignals() []os.Signal {
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if restartSignal != nil {
		sigs = append(sigs, restartSignal)
	}
	return sigs
}

// isRestartSignal reports whether sig means "restart", as opposed to
// "stop". Always false on platforms with no restart signal.
func isRestartSignal(sig os.Signal) bool {
	return restartSignal != nil && sig == restartSignal
}
