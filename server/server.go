/*

Launch blanket server

- Serves on a local port
- May change over to use unix sockets later

- some things may want access to task structs but are not going to be able to query the database directly
- define routes here, but write actual functions in other sub folders
*/

package server

import (
	"github.com/gin-gonic/gin"
	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/queue"
	"github.com/turtlemonvh/blanket/worker"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ginLogger is a drop-in replacement for gin-gonic/contrib/ginrus.
// It logs each request via logrus with the same fields ginrus would produce.
func ginLogger(logger *log.Logger, timeFormat string, utc bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		end := time.Now()
		if utc {
			end = end.UTC()
		}
		entry := logger.WithFields(log.Fields{
			"status":  c.Writer.Status(),
			"method":  c.Request.Method,
			"path":    path,
			"ip":      c.ClientIP(),
			"latency": end.Sub(start),
			"time":    end.Format(timeFormat),
		})
		if len(c.Errors) > 0 {
			entry.Error(c.Errors.String())
		} else {
			entry.Info()
		}
	}
}

type ServerConfig struct {
	DB             database.BlanketDB
	Q              queue.BlanketQueue
	ResultsPath    string
	Port           int
	TimeMultiplier float64
	Version        string
	TaskEvents     *EventHub
	WorkerEvents   *EventHub
	// SchedulerInterval controls how often the background scheduler loop
	// (server/scheduler.go) checks for due SCHEDULED tasks and RECURRING
	// templates. Zero means DefaultSchedulerInterval.
	SchedulerInterval time.Duration
	// SchedulerMaxScheduled bounds both how many SCHEDULED/RECURRING/
	// PAUSED tasks a single scheduler tick will scan (server/scheduler.go)
	// and how many are allowed to be live at once -- POST /task/ returns
	// 429 once accepting a new SCHEDULED or RECURRING submission would
	// reach this many. Zero means DefaultSchedulerMaxScheduled. Backed by
	// the scheduler.maxScheduled config key.
	SchedulerMaxScheduled int
	// ShutdownTimeout bounds the drain step of the shutdown sequence (see
	// server/lifecycle.go). Zero means DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
	// Cleanup, if set, is the last thing the shutdown sequence runs, after
	// the listener is closed, the tailers are stopped, and the background
	// loops have been cancelled. command/serve.go passes the BoltDB handle's
	// Close here: nothing above it in the teardown may still touch storage.
	Cleanup func()

	// ReaperEnabled turns the background reaper loop on
	// (turtlemonvh/blanket#23 phase 3). Backed by the `reaper.enabled`
	// config key, which defaults to **true** — the zero value here is
	// false because a ServerConfig built by hand in a test should not
	// silently acquire a loop that rewrites task state underneath it.
	// command/serve.go is the one place that reads the config key.
	ReaperEnabled bool
	// ReaperInterval is how often a reaper pass runs; the remaining
	// Reaper* fields are the thresholds each pass measures against. All
	// are unscaled durations — timeMultiplier is applied at use, via
	// lib/timing — and all fall back to the Default* constants in
	// server/reaper.go when zero.
	ReaperInterval         time.Duration
	ReaperWorkerStaleAfter time.Duration
	ReaperWorkerDeadAfter  time.Duration
	ReaperTaskStaleAfter   time.Duration
	ReaperMaxRequeues      int

	// BackupDir is where POST /ops/backup writes when the request names
	// no directory. Backed by the `storage.backupDir` config key; empty
	// means the database's own backups/ subdirectory
	// (bolt.DefaultBackupDir). turtlemonvh/blanket#23 phase 4.
	BackupDir string

	// The restart state machine's knobs (turtlemonvh/blanket#23 phase 5).
	// All four are read once, by command/serve.go, from `restart.*` config
	// keys and their matching flags; see server/restart.go for what they
	// mean and docs/upgrade.md for the operator's version.
	//
	// ExecMode is auto|exec|exit (brief decision row 2). DrainMode is
	// auto|always|never (row 3). Empty means the documented default in
	// both cases, so a hand-built ServerConfig behaves like a configured
	// one rather than like a broken one.
	ExecMode  string
	DrainMode string
	// DrainTimeout bounds how long a drain waits for stopped workers to
	// exit; RestartDeadline is how long the watchdog gives the driver
	// between transitions. Both unscaled — timing.Scale is applied at use
	// — and both fall back to the Default* constants when zero.
	DrainTimeout    time.Duration
	RestartDeadline time.Duration

	// restartExecFn performs the exec step for POST /ops/restart/exec. Set
	// by Serve to the running BlanketServer's restart trigger; nil in a
	// router built without one, which is what the handler reports as 503
	// rather than pretending to restart something that isn't listening.
	restartExecFn func(mode string)

	// spawnWorkerFn replaces the real daemon fork on the respawn path.
	// Tests only: the real one resolves os.Executable(), which under
	// `go test` is the test binary. See spawnRespawnedWorker.
	spawnWorkerFn func(*worker.WorkerConf) (worker.WorkerConf, error)

	// instanceMu guards the two facts a restarting server has to be able
	// to tell a worker about: which process it is, and when that process
	// started. Both are generated lazily on first use so a hand-built
	// ServerConfig needs no extra setup, and both are stable for the life
	// of the process. Phase 4 additionally persists the pair into the
	// meta bucket at startup (persistInstance, called from Serve), so the
	// identity a worker last saw outlives the process that had it.
	instanceMu      sync.Mutex
	instanceId      string
	instanceStarted int64

	// restartMu guards the in-memory projection of the restart record —
	// the three facts derived from it that are read on hot paths. All
	// three are written together by applyRestartRecord
	// (server/restart.go), from the record, so they cannot drift from it
	// or from each other.
	//
	// restartPendingFlag is the third of the reaper's grace layers: while
	// a restart is in flight, every worker is about to look stale through
	// no fault of its own, so no pass runs at all.
	//
	// spawnPaused refuses worker spawn from PAUSED onward, closing the
	// window in which this server could fork a worker out of a binary
	// that has already been swapped underneath it.
	//
	// restartDeadlineTs is the watchdog's copy of the record's deadline,
	// so the loop's common case — no restart at all — costs a mutex and
	// a comparison rather than a database read.
	restartMu          sync.Mutex
	restartPendingFlag bool
	spawnPaused        bool
	restartDeadlineTs  int64

	// shutdownCh is closed once the server starts shutting down. Every
	// streaming (SSE) handler selects on it and returns promptly -- without
	// that, net/http's Shutdown waits forever on an open stream, since it
	// never force-closes an active connection. Lazily created so a
	// ServerConfig built by hand (tests, GetRouter-only use) needs no extra
	// setup. Guarded by shutdownMu.
	shutdownMu sync.Mutex
	shutdownCh chan struct{}
}

// InstanceId returns this server process's instance id, generating one on
// first use.
//
// It exists so a worker can notice that the server it is talking to is not
// the one it was talking to a minute ago. The heartbeat response carries
// it, and a worker that sees it change knows a restart happened underneath
// it — which matters because the worker's own view of "am I stopped?" and
// any in-flight transition were established against the previous process.
// Today the worker only logs the change; phase 5 acts on it.
func (s *ServerConfig) InstanceId() string {
	s.instanceMu.Lock()
	defer s.instanceMu.Unlock()
	if s.instanceId == "" {
		s.instanceId = objectid.NewObjectId().Hex()
		s.instanceStarted = time.Now().Unix()
	}
	return s.instanceId
}

// StartedTs returns the unix time this server process started serving.
//
// The reaper measures staleness from max(LastHeardTs, StartedTs): after a
// restart no worker could have reported for however long the server was
// down, so without this floor every one of them would look stale at once
// and the first pass after a restart would be the most destructive one.
func (s *ServerConfig) StartedTs() int64 {
	s.InstanceId() // both are set together, on first use
	s.instanceMu.Lock()
	defer s.instanceMu.Unlock()
	return s.instanceStarted
}

// persistInstance writes this process's instance id and start time into
// the meta bucket (turtlemonvh/blanket#23 phase 4). Called once from
// Serve.
//
// Phase 3 kept the pair in memory only, which answered "did the server
// restart?" for anyone holding a connection across the restart, but
// nothing at all for anyone arriving afterwards. Persisting it means the
// last identity is readable from the file, which is what phase 5's restart
// verification and phase 6's upgrade check need: "is the server that came
// back a different process from the one I stopped?" is otherwise
// unanswerable from outside.
//
// A failure here is logged and swallowed. The in-memory pair is the one
// every live code path reads; the persisted copy is for the next process.
func (s *ServerConfig) persistInstance() {
	if s.DB == nil {
		return
	}
	if err := s.DB.SetServerInstance(database.ServerInstance{
		InstanceId: s.InstanceId(),
		StartedTs:  s.StartedTs(),
	}); err != nil {
		log.WithField("err", err).Warn("could not persist this server instance's identity")
	}
}

// SetRestartPending suppresses the reaper while a restart is in flight, and
// releases it afterwards. Phase 5's restart state machine is the caller;
// see the reaper's grace layers in server/reaper.go.
func (s *ServerConfig) SetRestartPending(pending bool) {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	s.restartPendingFlag = pending
}

// restartPending reports whether a restart is in flight.
func (s *ServerConfig) restartPending() bool {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	return s.restartPendingFlag
}

// shutdownChan returns the channel that is closed when the server begins
// shutting down. Streaming handlers select on it; see sseStream (ui.go) and
// streamLog (serve_logs.go).
func (s *ServerConfig) shutdownChan() <-chan struct{} {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	if s.shutdownCh == nil {
		s.shutdownCh = make(chan struct{})
	}
	return s.shutdownCh
}

// signalShutdown closes the shutdown channel, releasing every open
// streaming handler. Idempotent.
func (s *ServerConfig) signalShutdown() {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	if s.shutdownCh == nil {
		s.shutdownCh = make(chan struct{})
	}
	select {
	case <-s.shutdownCh:
	default:
		close(s.shutdownCh)
	}
}

func (s *ServerConfig) GetRouter() *gin.Engine {
	if s.TaskEvents == nil {
		s.TaskEvents = NewEventHub()
	}
	if s.WorkerEvents == nil {
		s.WorkerEvents = NewEventHub()
	}

	// https://godoc.org/github.com/rs/cors
	c := cors.New(cors.Options{
		AllowedOrigins:     []string{"*"},
		AllowedMethods:     []string{"GET", "POST", "PUT", "DELETE"},
		OptionsPassthrough: false,
	})

	// If we don't return early from handler function we get a 404 for the options request
	//
	// /ops/ is carved out of this entirely (turtlemonvh/blanket#23 phase
	// 4). The wildcard policy above would otherwise approve the CORS
	// preflight that the ops endpoints' required X-Blanket-Restart header
	// exists to *provoke*, which would let any page the user visits drive
	// them from their browser. With no Access-Control-Allow-Origin header
	// written, the preflight falls through to a 404 and the browser
	// refuses to send the real request. See server/serve_ops.go.
	makeCorsHandler := func(c *cors.Cors) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, OpsPathPrefix) {
				return
			}
			c.HandlerFunc(w, r)
			// Allow it to return to avoid a 404
			if r.Method == "OPTIONS" && w.Header().Get("Access-Control-Allow-Origin") == r.Header.Get("Origin") {
				w.WriteHeader(http.StatusOK)
			}
		}
	}

	if log.GetLevel() != log.DebugLevel {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(ginLogger(log.StandardLogger(), time.RFC3339, true))
	r.Use(gin.Recovery())
	r.Use(gin.WrapF(makeCorsHandler(c)))

	// Make the result dir browseable
	r.StaticFS("/results", gin.Dir(s.ResultsPath, true))

	// HTMX + Go-template UI.
	r.StaticFS("/ui/static", uiStaticFS())
	r.GET("/ui/", s.uiTasksPage)
	r.GET("/ui/tasks/:id", s.uiTaskDetailPage) // also serves the series detail view for a cron template
	r.GET("/ui/upcoming", s.uiUpcomingPage)
	r.GET("/ui/workers", s.uiWorkersPage)
	r.GET("/ui/workers/:id", s.uiWorkerDetailPage)
	r.GET("/ui/task-types", s.uiTaskTypesPage)
	r.GET("/ui/task-types/:name", s.uiTaskTypeDetailPage)
	r.GET("/ui/about", s.uiAboutPage)
	r.POST("/ui/tasks", s.uiSubmitTask)
	r.POST("/ui/workers", s.uiSubmitWorker)
	r.GET("/ui/partials/tasks-rows", s.uiTasksRowsPartial)
	r.GET("/ui/partials/workers-rows", s.uiWorkersRowsPartial)
	r.GET("/ui/partials/task-types-rows", s.uiTaskTypesRowsPartial)
	r.GET("/ui/partials/new-task", s.uiNewTaskPartial)
	r.GET("/ui/partials/task-type-env", s.uiTaskTypeEnvPartial)
	r.GET("/ui/partials/task-type-warnings", s.uiTaskTypeWarningsPartial)
	r.GET("/ui/partials/schedule-preview", s.uiSchedulePreviewPartial)
	r.GET("/ui/partials/form-error", s.uiFormErrorPartial)
	r.GET("/ui/partials/custom-env-row", s.uiCustomEnvRowPartial)
	r.GET("/ui/partials/new-worker", s.uiNewWorkerPartial)
	r.GET("/ui/partials/blank", s.uiBlankPartial)
	r.GET("/ui/partials/upcoming-onetime-rows", s.uiUpcomingOneTimeRowsPartial)
	r.GET("/ui/partials/upcoming-series-rows", s.uiUpcomingSeriesRowsPartial)
	r.GET("/ui/partials/series-schedule", s.uiSeriesSchedulePartial) // ?id=<template id>
	// Series lifecycle actions. Thin form-friendly wrappers over the same
	// functions PUT /task/:id/{pause,resume,cancel,schedule} call, whose
	// response is the re-rendered schedule block (inline error included)
	// rather than a JSON status. See server/ui_schedule.go.
	r.PUT("/ui/series/:id/pause", s.uiSeriesPause)
	r.PUT("/ui/series/:id/resume", s.uiSeriesResume)
	r.PUT("/ui/series/:id/cancel", s.uiSeriesCancel)
	r.PUT("/ui/series/:id/schedule", s.uiSeriesChangeSchedule)
	r.GET("/ui/sse/tasks", s.sseTaskEvents)
	r.GET("/ui/sse/workers", s.sseWorkerEvents)

	// Redirect to ui
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/ui/")
	})
	r.GET("/version", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"version": s.Version,
			"name":    "blanket",
			"author":  "Timothy Van Heest <timothy.vanheest@gmail.com>",
		})
	})

	r.GET("/ops/status/", MetricsHandler)
	// Mutating ops endpoints are loopback-only and require the
	// X-Blanket-Restart header. See server/serve_ops.go for why all three
	// layers are needed, and server/serve_restart.go for why the restart
	// machine is one route per transition.
	//
	// GET /ops/restart/status is behind the same guard as the mutating
	// ones, unlike GET /ops/status/. It reports the pid, the resolved exec
	// mode, and whether the process thinks it is supervised — a map of how
	// to interfere with this server, which is not something to hand to any
	// page the user happens to have open.
	r.POST("/ops/backup", opsGuard(), s.opsBackup)
	r.GET("/ops/restart/status", opsGuard(), s.opsRestartStatus)
	r.POST("/ops/restart/begin", opsGuard(), s.opsRestartBegin)
	r.POST("/ops/restart/pause", opsGuard(), s.opsRestartPause)
	r.POST("/ops/restart/swapped", opsGuard(), s.opsRestartSwapped)
	r.POST("/ops/restart/drain", opsGuard(), s.opsRestartDrain)
	r.POST("/ops/restart/exec", opsGuard(), s.opsRestartExec)
	r.POST("/ops/restart/abort", opsGuard(), s.opsRestartAbort)
	r.GET("/config/", s.getConfigProcessed)

	r.GET("/task_type/", s.getTaskTypes)
	r.GET("/task_type/:name", s.getTaskType)

	// Called by user
	r.GET("/task/", s.getTasks)                       // list tasks in db
	r.GET("/task/:id", s.getTask)                     // fetch just 1 by id
	r.POST("/task/", s.postTask)                      // add a new task to the queue
	r.DELETE("/task/:id", s.removeTask)               // delete all information from db, including killing if running
	r.GET("/task/:id/log", s.streamTaskLog)           // stream stdout log
	r.GET("/task/:id/log/tail", s.tailTaskLog)        // last N lines of stdout
	r.PUT("/task/:id/cancel", s.cancelTask)           // stop execution of a task; will be moved to state STOPPED
	r.PUT("/task/:id/pause", s.pauseTask)             // pause a RECURRING template; sets pausedTs
	r.PUT("/task/:id/resume", s.resumeTask)           // resume a PAUSED template back to RECURRING
	r.PUT("/task/:id/schedule", s.changeTaskSchedule) // change a SCHEDULED task's notBefore, or a RECURRING/PAUSED template's cron
	r.GET("/schedule/describe", s.describeSchedule)   // {"cron": ..., "description": ..., "next": [...]}; live preview for a create form

	// Called by worker
	r.POST("/task/claim/:workerid", s.claimTask)      // claim a task
	r.PUT("/task/:id/run", s.markTaskAsRunning)       // mark a task as running
	r.PUT("/task/:id/progress", s.updateTaskProgress) // update progress
	r.PUT("/task/:id/finish", s.markTaskAsFinished)   // update state

	r.GET("/worker/:id", s.getWorker)
	r.GET("/worker/", s.getWorkers)
	r.POST("/worker/", s.launchNewWorker)             // called from front end, doesn't actually hit database
	r.PUT("/worker/:id/heartbeat", s.heartbeatWorker) // worker liveness ping; server stamps lastHeardTs
	r.PUT("/worker/:id/stop", s.stopWorker)           // stop/pause worker; will stop after current task stops
	r.PUT("/worker/:id/restart", s.restartWorker)     // re-start an existing worker
	r.PUT("/worker/:id", s.updateWorker)              // used for initial creation + status updates
	r.DELETE("/worker/:id", s.deleteWorker)           // remove from database; can only be called on a stopped worker
	r.GET("/worker/:id/logs", s.getWorkerLogfile)     // full logfile download
	r.GET("/worker/:id/log", s.streamWorkerLog)       // SSE stream of worker log
	r.GET("/worker/:id/log/tail", s.tailWorkerLog)    // last N lines of worker log

	if h := s.mcpHTTPHandler(); h != nil {
		r.Any("/mcp", gin.WrapH(h))
	}

	return r
}
