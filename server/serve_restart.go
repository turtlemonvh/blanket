package server

/*

The /ops/restart/* endpoints (turtlemonvh/blanket#23 phase 5).

One route per transition, rather than one route taking a target state.
Three reasons, in ascending order of how much they matter:

  - the manual recipe in docs/upgrade.md is a list of curl commands a human
    reads and adapts, and `POST /ops/restart/pause` says what it does in a
    way `POST /ops/restart -d '{"state":"PAUSED"}'` does not;
  - the transitions are not interchangeable. `drain` stops processes and
    can wait for them, `exec` never returns a normal response, `abort`
    runs backwards through anything the others did. A single route would
    be a switch statement wearing a REST costume;
  - phase 6's `--print-plan` prints the sequence it is about to run.
    Printing a list of URLs is printing the plan.

BACKED_UP has no route of its own: POST /ops/backup advances the record
when one is in flight, so the state records something the server watched
happen rather than something a caller asserted. SWAPPED does have one,
because swapping a file on disk is the single step the server genuinely
cannot observe.

Every route here sits behind opsGuard (loopback + X-Blanket-Restart, carved
out of the wildcard CORS policy) — see server/serve_ops.go. These are the
endpoints that guard exists for; backup was just the first customer.

*/

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
)

// restartStatusResponse is what GET /ops/restart/status answers with.
//
// It carries the resolved modes as well as the record because "what will
// happen when I call exec?" is not answerable from the record alone on a
// server whose flags you cannot see, and the person asking is typically
// mid-upgrade at an hour when guessing is expensive.
type restartStatusResponse struct {
	Restart          database.RestartRecord `json:"restart"`
	Active           bool                   `json:"active"`
	SpawnPaused      bool                   `json:"spawnPaused"`
	ExecMode         string                 `json:"execMode"`
	ResolvedExecMode string                 `json:"resolvedExecMode"`
	DrainMode        string                 `json:"drainMode"`
	DrainTimeoutMs   int64                  `json:"drainTimeoutMs"`
	Supervised       bool                   `json:"supervised"`
	InstanceId       string                 `json:"instanceId"`
	Pid              int                    `json:"pid"`
	Version          string                 `json:"version"`
}

// opsRestartStatus handles GET /ops/restart/status.
func (s *ServerConfig) opsRestartStatus(c *gin.Context) {
	rr, err := s.DB.RestartRecord()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rr.State == "" {
		rr.State = database.RestartStateIdle
	}

	c.JSON(http.StatusOK, restartStatusResponse{
		Restart:          rr,
		Active:           rr.Active(),
		SpawnPaused:      s.spawnIsPaused(),
		ExecMode:         s.execMode(),
		ResolvedExecMode: resolveExecMode(s.execMode()),
		DrainMode:        s.drainMode(),
		DrainTimeoutMs:   s.drainTimeout().Milliseconds(),
		Supervised:       Supervised(),
		InstanceId:       s.InstanceId(),
		Pid:              processPid(),
		Version:          s.Version,
	})
}

// opsRestartBegin handles POST /ops/restart/begin: IDLE -> STAGED.
//
// A body is optional. With none, this is "restart this server, reason
// unrecorded, using the configured exec mode" — which is the shape a human
// typing curl at 2am wants, and the shape phase 6 will send with every
// field filled in.
func (s *ServerConfig) opsRestartBegin(c *gin.Context) {
	opts := BeginRestartOptions{}
	if c.Request.Body != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&opts); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	if reason := c.Query("reason"); reason != "" {
		opts.Reason = reason
	}
	if mode := c.Query("execMode"); mode != "" {
		opts.ExecMode = mode
	}

	rr, err := s.beginRestart(opts)
	if err != nil {
		c.JSON(restartStatusCode(err), gin.H{"error": err.Error(), "restart": rr})
		return
	}
	c.JSON(http.StatusOK, gin.H{"restart": rr})
}

// opsRestartPause handles POST /ops/restart/pause: -> PAUSED.
//
// From here on the server refuses to spawn workers with 409. That is the
// point of the state and the reason it is its own call rather than folded
// into `begin`: the backup between them can take a while on a large
// database, and refusing worker spawn for the length of a backup would be
// a cost with nothing to show for it.
func (s *ServerConfig) opsRestartPause(c *gin.Context) {
	rr, err := s.advanceRestart(database.RestartStatePaused, nil)
	if err != nil {
		c.JSON(restartStatusCode(err), gin.H{"error": err.Error(), "restart": rr})
		return
	}
	c.JSON(http.StatusOK, gin.H{"restart": rr, "spawnPaused": s.spawnIsPaused()})
}

// opsRestartSwapped handles POST /ops/restart/swapped: -> SWAPPED.
//
// The one state that is purely the caller's word. The server has no way to
// tell that the file behind os.Executable() is a different build from the
// one it is running — comparing an inode or an mtime would be guessing, and
// guessing wrong in the safe-looking direction ("it hasn't changed") is
// what would let a stale binary be declared swapped.
func (s *ServerConfig) opsRestartSwapped(c *gin.Context) {
	rr, err := s.advanceRestart(database.RestartStateSwapped, nil)
	if err != nil {
		c.JSON(restartStatusCode(err), gin.H{"error": err.Error(), "restart": rr})
		return
	}
	c.JSON(http.StatusOK, gin.H{"restart": rr})
}

// opsRestartDrain handles POST /ops/restart/drain: -> DRAINING.
//
// `?wait=false` returns as soon as the transaction commits; the default
// waits up to the drain timeout for the stopped workers' processes to
// actually exit. Either way the response is 200: a drain that timed out
// with stragglers is information, not a failure — the caller may well
// decide a worker that is three hours into a render is a reason to
// postpone the restart, and that decision is not the server's to make.
func (s *ServerConfig) opsRestartDrain(c *gin.Context) {
	wait := c.Query("wait") != "false"

	res, err := s.drainRestart(c.Request.Context(), wait)
	if err != nil {
		c.JSON(restartStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// opsRestartExec handles POST /ops/restart/exec: -> EXECING, then the
// server goes away.
//
// The response is written and flushed *before* the shutdown starts. A
// handler that tore its own listener down mid-response would leave the
// caller unable to distinguish "the restart began" from "the connection
// broke", which is exactly the distinction the caller is about to spend
// the next thirty seconds trying to make.
func (s *ServerConfig) opsRestartExec(c *gin.Context) {
	if s.drainMode() == database.DrainModeAlways {
		if rr, _ := s.DB.RestartRecord(); rr.Active() &&
			database.RestartStateRank(rr.State) < database.RestartStateRank(database.RestartStateDraining) {
			if _, err := s.drainRestart(c.Request.Context(), true); err != nil {
				c.JSON(restartStatusCode(err), gin.H{"error": err.Error()})
				return
			}
		}
	}

	rr, err := s.advanceRestart(database.RestartStateExecing, nil)
	if err != nil {
		c.JSON(restartStatusCode(err), gin.H{"error": err.Error(), "restart": rr})
		return
	}

	mode := resolveExecMode(rr.ExecMode)
	trigger := s.restartExecFn
	if trigger == nil {
		// GetRouter is reachable without Serve (every handler test builds
		// a router directly), so there may be no running server to stop.
		// Say so rather than pretending the restart happened.
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "this server has no running listener to restart",
			"restart": rr,
		})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"restart":  rr,
		"execMode": mode,
		"exitCode": execModeExitCode(mode),
		"pid":      processPid(),
	})
	c.Writer.Flush()

	log.WithFields(log.Fields{
		"restartId": rr.Id,
		"execMode":  mode,
	}).Warn("restart: exec requested; shutting down")

	// Off the request goroutine: the shutdown sequence drains in-flight
	// requests, and this request is one of them.
	go trigger(mode)
}

// opsRestartAbort handles POST /ops/restart/abort: anything -> IDLE.
//
// Idempotent in the way that matters: aborting when nothing is in flight
// is a 409 with a plain explanation rather than an error a script has to
// parse, and aborting after a drain brings the drained workers back.
func (s *ServerConfig) opsRestartAbort(c *gin.Context) {
	reason := c.Query("reason")
	if reason == "" {
		reason = "aborted by POST /ops/restart/abort"
	}

	was, err := s.abortRestart(reason)
	if err != nil {
		c.JSON(restartStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"aborted": was,
		"respawned": database.RestartStateRank(was.State) >=
			database.RestartStateRank(database.RestartStateDraining),
	})
}
