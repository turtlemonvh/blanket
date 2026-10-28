package worker

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/timing"
)

// Shutdown budgets for a worker that receives SIGTERM or SIGINT. Unscaled;
// timeMultiplier is applied at use. Vars so tests can shorten them.
var (
	// ShutdownRetryDeadline is the total time, measured from the signal,
	// the worker spends telling the server how things ended: the killed
	// task's final state, then its own stopped record. Both calls share
	// it, so a server that is down costs this once, not once per call.
	//
	// It is short because nothing depends on these reports landing. A
	// server that can't be reached will have its reaper settle the task
	// (from the outcome journal) and the worker record (from the dead
	// pid). What must not happen is the worker outliving its signal: a
	// supervisor waiting for it to exit is often the very thing keeping
	// the server down, so retrying against that server only deadlocks.
	ShutdownRetryDeadline = 3 * time.Second

	// ShutdownExitDeadline is the backstop: if the worker is still alive
	// this long after the signal, it kills its task and exits regardless
	// of what it was waiting on. The normal path finishes well inside it
	// (ShutdownRetryDeadline plus the combined log's short tail grace).
	ShutdownExitDeadline = 10 * time.Second
)

// shutdown is a worker process's local record of a signal-requested stop.
//
// A signal means "stop now", not "stop after this task" (that is what a
// server-side stop, as a restart drain uses, is for). So requesting a
// shutdown does three things at once: it sets the flag the claim loop exits
// on, cancels the context every in-flight server call runs under, and kills
// the running task's process.
//
// Held by pointer from WorkerConf so WorkerConf stays copyable (see the
// comment on WorkerConf.sd).
type shutdown struct {
	requested atomic.Bool

	// ctx is what the worker's ordinary server calls run under. It is
	// cancelled on request, so a call stuck on a hung server returns at
	// once instead of after its own timeout.
	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// child is the running task's process, nil between tasks.
	child *os.Process
	// killed is the process request() killed, so ProcessOne can tell "the
	// worker stopped this task" from "the task died on its own".
	killed *os.Process
	// reportCtx bounds the final reports. It is created on request, not
	// derived from ctx, because ctx is cancelled by then.
	reportCtx    context.Context
	reportCancel context.CancelFunc
}

func newShutdown() *shutdown {
	ctx, cancel := context.WithCancel(context.Background())
	return &shutdown{ctx: ctx, cancel: cancel}
}

// request starts the shutdown. It reports false if one had already been
// requested, so a second signal can be told apart from the first.
func (s *shutdown) request() bool {
	if !s.requested.CompareAndSwap(false, true) {
		return false
	}
	s.mu.Lock()
	// The cancel func is kept only to satisfy go vet: the deadline
	// releases the context's resources, and the process is exiting anyway.
	s.reportCtx, s.reportCancel = context.WithDeadline(context.Background(),
		time.Now().Add(timing.Scale(ShutdownRetryDeadline)))
	s.mu.Unlock()
	s.cancel()
	s.killChild()
	return true
}

// killChild kills the running task, if there is one. It uses the same
// Process.Kill the STOPPED-tombstone and timeout paths use, so a task
// stopped by a signal ends exactly as one stopped through the API does.
func (s *shutdown) killChild() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil || s.killed == s.child {
		return
	}
	log.WithFields(log.Fields{
		"pid": s.child.Pid,
	}).Warn("killing the running task because the worker is shutting down")
	if err := s.child.Kill(); err != nil {
		log.WithFields(log.Fields{
			"pid": s.child.Pid,
			"err": err.Error(),
		}).Warn("failed to kill the running task")
	}
	s.killed = s.child
}

// track records p as the running task. A shutdown that has already been
// requested kills it straight away: the signal landed between the claim
// and the start, and the task must not outlive the worker.
func (s *shutdown) track(p *os.Process) {
	s.mu.Lock()
	s.child = p
	s.mu.Unlock()
	if s.requested.Load() {
		s.killChild()
	}
}

func (s *shutdown) untrack() {
	s.mu.Lock()
	s.child = nil
	s.mu.Unlock()
}

// killedTask reports whether the shutdown killed p.
func (s *shutdown) killedTask(p *os.Process) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return p != nil && s.killed == p
}

// reportContext is the context the final reports run under: the shared
// ShutdownRetryDeadline budget once a shutdown is requested, unbounded
// (Background) before then.
func (s *shutdown) reportContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reportCtx == nil {
		return context.Background()
	}
	return s.reportCtx
}
