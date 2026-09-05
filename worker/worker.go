package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kardianos/osext"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/lib/httpx"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/lib/timing"
	"github.com/turtlemonvh/blanket/tasks"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"
)

const (
	DEFAULT_CHECK_INTERVAL_SECONDS = 2
	// MIN_CHECK_INTERVAL_SECONDS is the lowest check interval a worker can
	// be configured with. Below this, the claim/refresh loop hammers the
	// server with no useful work — see ProcessTasks.
	MIN_CHECK_INTERVAL_SECONDS = 0.5

	// MAX_POLL_BACKOFF_SECONDS caps the full-jitter backoff the claim loop
	// applies while the server is unreachable. Unscaled; timeMultiplier is
	// applied at use.
	MAX_POLL_BACKOFF_SECONDS = 30
)

// Retry budgets for a worker's own registration/lifecycle calls
// (turtlemonvh/blanket#23 phase 1). Unscaled constants — timeMultiplier is
// applied inside lib/httpx. Vars so tests can shorten them.
var (
	// RegisterRetryDeadline bounds the worker's initial registration. A
	// worker started while the server is briefly down (a supervisor
	// restarting both at once, say) should wait rather than exit.
	RegisterRetryDeadline = 30 * time.Second

	// ShutdownRetryDeadline bounds how long the SIGTERM handler keeps
	// trying to register the worker as stopped before giving up. It must
	// stay finite: the shutdown path has to terminate the process even
	// when the server is never coming back, which is exactly the case the
	// pre-fix handler spun on forever.
	ShutdownRetryDeadline = 10 * time.Second
)

// ErrCheckIntervalTooLow is returned by Run when CheckInterval is set to
// a positive value below MIN_CHECK_INTERVAL_SECONDS. Callers (HTTP handlers,
// CLI) should surface it to the user instead of silently clamping.
var ErrCheckIntervalTooLow = fmt.Errorf("checkInterval must be >= %.1fs", MIN_CHECK_INTERVAL_SECONDS)

// Worker

// CLean up id and parsed tags' parse these in cli

type WorkerConf struct {
	Id            objectid.ObjectId `json:"id"`
	Tags          []string          `json:"tags"`
	Logfile       string            `json:"logfile"`
	Daemon        bool              `json:"daemon"`
	Pid           int               `json:"pid"`
	Stopped       bool              `json:"stopped"`
	CheckInterval float64           `json:"checkInterval"` // seconds
	StartedTs     int64             `json:"startedTs"`
	// LastHeardTs is the unix timestamp of the last time the server heard
	// from (or acted on) this worker record — currently bumped when the
	// worker is stopped. Intended to grow into a general heartbeat field;
	// see the "not heartbeated in a while" FIXME on CleanupStalledWorkers.
	LastHeardTs int64 `json:"lastHeardTs"`

	// stopping is a purely local shutdown flag, set by the SIGTERM/SIGINT
	// handler (turtlemonvh/blanket#23 phase 1). The claim loop's exit
	// condition used to be Stopped alone, which the worker only learns
	// about by asking the server — so a SIGTERM delivered while the server
	// was unreachable did nothing at all and the worker ran forever.
	//
	// A pointer rather than a value so WorkerConf stays copyable: the
	// struct is passed and returned by value all over the server, and an
	// inlined atomic.Bool would trip `go vet`'s copylocks check. Nil means
	// "nothing has requested a stop" (the zero value for every WorkerConf
	// that isn't running its own Run loop).
	stopping *atomic.Bool `json:"-"`
}

// stopRequested reports whether this process has been asked to shut down by
// a signal. Safe on a zero-valued WorkerConf.
func (c *WorkerConf) stopRequested() bool {
	return c.stopping != nil && c.stopping.Load()
}

// buildDaemonCmd constructs the exec.Cmd used to relaunch this process as
// a detached worker daemon (see the Daemon branch of Run, below). Forwards
// the config file and port this process itself resolved via
// InitializeConfig, so the daemonized child talks to the same server the
// parent did instead of falling back to viper's own default resolution —
// see https://github.com/turtlemonvh/blanket/issues/45. --port is always
// forwarded (it has a default even with no config file); --config is only
// forwarded when a config file was actually resolved, since an empty
// value would make the child fail to parse its own flags.
func (c *WorkerConf) buildDaemonCmd(path string) *exec.Cmd {
	cmd := exec.Command(path, "worker")
	if len(c.Tags) != 0 {
		cmd.Args = append(cmd.Args, "--tags")
		cmd.Args = append(cmd.Args, strings.Join(c.Tags, ","))
	}
	if !c.Id.IsZero() {
		cmd.Args = append(cmd.Args, "--id")
		cmd.Args = append(cmd.Args, c.Id.Hex())
	}
	if c.Logfile != "" {
		cmd.Args = append(cmd.Args, "--logfile")
		cmd.Args = append(cmd.Args, c.Logfile)
	}
	if c.CheckInterval != 0 {
		cmd.Args = append(cmd.Args, "--checkinterval")
		cmd.Args = append(cmd.Args, fmt.Sprintf("%f", c.CheckInterval))
	}
	if cfgFile := viper.ConfigFileUsed(); cfgFile != "" {
		cmd.Args = append(cmd.Args, "--config")
		cmd.Args = append(cmd.Args, cfgFile)
	}
	cmd.Args = append(cmd.Args, "--port")
	cmd.Args = append(cmd.Args, fmt.Sprintf("%d", viper.GetInt("port")))
	return cmd
}

// FIXME: Ensure this works ok on windows: https://golang.org/pkg/os/#Signal
// FIXME: Make sure logging works fine with sighup for logrotate
// https://en.wikipedia.org/wiki/Unix_signal#POSIX_signals
func (c *WorkerConf) Run() error {
	var err error

	// Initialize
	c.StartedTs = time.Now().Unix()
	if c.Id.IsZero() {
		// Allow users to pass in existing ids to re-use old worker configs
		c.Id = objectid.NewObjectId()
	}

	// Treat 0 as "use default", but reject anything below the minimum to
	// keep the claim loop from hammering the server.
	if c.CheckInterval == 0 {
		c.CheckInterval = DEFAULT_CHECK_INTERVAL_SECONDS
	}
	if c.CheckInterval < MIN_CHECK_INTERVAL_SECONDS {
		return ErrCheckIntervalTooLow
	}

	if c.Daemon {
		path, err := osext.Executable()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Error("Problem getting executable path")
			return err
		}

		log.WithFields(log.Fields{
			"path": path,
		}).Debug("Path to current executable is")

		cmd := c.buildDaemonCmd(path)
		setDaemonAttrs(cmd)

		// FIXME: Redirect the first couple seconds of stdout here to check that process started ok
		cmd.Start()

		log.WithFields(log.Fields{
			"tags":          c.Tags,
			"pid":           cmd.Process.Pid,
			"checkInterval": c.CheckInterval,
			"logfile":       c.Logfile,
		}).Info("Starting daemonized executable")

	} else {
		// Handle clean shutdown.
		//
		// Two bugs used to live here (turtlemonvh/blanket#23 phase 1). The
		// retry loop never reassigned err and never incremented its
		// counter, so a server that was down turned this into an infinite
		// spin — the exact situation an upgrade creates. And it wrote to
		// Run's `err` from a second goroutine while the main one was using
		// it, which is a data race.
		//
		// Retrying now happens inside StopWorkerById (full-jitter backoff
		// against a finite deadline), and the handler owns nothing the main
		// goroutine touches: it reads a copy of the worker id taken before
		// the goroutine starts, because ProcessTasks overwrites *c on every
		// Refetch.
		c.stopping = &atomic.Bool{}
		workerId := c.Id
		shutdownChan := make(chan os.Signal, 1)
		signal.Notify(shutdownChan, os.Interrupt)
		signal.Notify(shutdownChan, syscall.SIGTERM)
		go func() {
			<-shutdownChan
			log.Warn("Received shutdown signal; attempting to set worker to 'stopped'")

			if serr := StopWorkerById(workerId); serr != nil {
				// Worker exits anyway, via the local flag below. Leaving a
				// record that says "running" is a problem for the server's
				// reaper to notice, not a reason to stay alive.
				log.WithFields(log.Fields{
					"err":      serr.Error(),
					"deadline": ShutdownRetryDeadline,
				}).Error("Failed to register worker as 'stopped'. Will exit anyway.")
			} else {
				log.Info("Successfully registered worker as 'stopped'.")
			}

			// Local stop flag: the claim loop exits on this even if the
			// server is never reachable again, so SIGTERM always
			// terminates the worker.
			c.stopping.Store(true)
		}()

		c.Pid = os.Getpid()

		err = c.SetLogfileName()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Fatal("Failed to set logfile name")
		}

		// Setup logfile; closes when process exits
		var f *os.File
		f, err = os.Create(c.Logfile)
		if err != nil {
			log.WithFields(log.Fields{
				"logfile": c.Logfile,
				"error":   err.Error(),
			}).Fatal("Failed to create worker logfile")
		}
		defer f.Close()

		// Log json output to file
		// All logs before this go to stdout
		log.SetFormatter(&log.JSONFormatter{})
		log.SetOutput(f)

		log.WithFields(log.Fields{
			"tags":          c.Tags,
			"pid":           c.Pid,
			"id":            c.Id.Hex(),
			"checkInterval": c.CheckInterval,
			"logfile":       c.Logfile,
		}).Info("Starting executable")

		// FIXME: Fire off heatbeat in a goroutine
		// - checks if worker should be paused or shut down
		// - do this instead of sending a signal from the parent process
		// - keep track of last heartbeat
		// - last task run should be searchable via the tasks
		go func() {
			// Will stop when worker process shuts down
			return
		}()

		c.MustRegister()
		if err := c.ProcessTasks(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	return nil
}

func (c *WorkerConf) SetLogfileName() error {
	if c.Logfile != "" {
		return nil
	}

	tmpl, err := template.New("logfile").Parse(viper.GetString("workers.logfileNameTemplate"))
	if err != nil {
		return err
	}
	var logfileNameBts bytes.Buffer
	err = tmpl.Execute(&logfileNameBts, c)
	if err != nil {
		return err
	}

	c.Logfile = logfileNameBts.String()
	return nil
}

// Registers itself in the database
// Must be ok or it will exit immediately (Fatal log)
// Also register with time running
func (c *WorkerConf) MustRegister() {
	c.Stopped = false

	err := c.UpdateInDatabase()
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Fatal("problem updating worker status in database")
	}
}

func workerURL(workerId objectid.ObjectId, suffix string) string {
	return fmt.Sprintf("http://localhost:%d/worker/%s%s", viper.GetInt("port"), workerId.Hex(), suffix)
}

// StopWorkerById asks the server to mark a worker stopped, retrying
// transient failures with full-jitter backoff until ShutdownRetryDeadline.
//
// Takes an id rather than a *WorkerConf because its caller is the signal
// handler, which must not read a struct the claim loop is concurrently
// overwriting.
func StopWorkerById(workerId objectid.ObjectId) error {
	_, err := httpx.Do(context.Background(), "PUT", workerURL(workerId, "/stop"), nil,
		httpx.Policy{Deadline: ShutdownRetryDeadline})
	return err
}

func (c *WorkerConf) Stop() error {
	return StopWorkerById(c.Id)
}

func (c *WorkerConf) UpdateInDatabase() error {
	bts, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = httpx.Do(context.Background(), "PUT", workerURL(c.Id, ""), bts,
		httpx.Policy{Deadline: RegisterRetryDeadline})
	return err
}

// Refetch pulls this worker's record from the server, overwriting the local
// copy — this is how the worker learns it has been stopped.
//
// Deliberately does not retry: the claim loop that calls it is itself the
// retry, with its own jittered backoff.
func (c *WorkerConf) Refetch() error {
	res, err := httpx.DoOnce(context.Background(), "GET", workerURL(c.Id, ""), nil, httpx.DefaultRequestTimeout)
	if err != nil {
		return err
	}
	return json.Unmarshal(res.Body, c)
}

func (c *WorkerConf) CheckIntervalMs() time.Duration {
	return timing.ScaleSeconds(c.CheckInterval)
}

// pollSleep waits out one claim-loop interval, plus a little jitter.
//
// The jitter (up to a quarter of an interval on top, never less than a full
// interval) exists so that a machine's workers don't stay phase-locked with
// each other after they all start or all reconnect at the same instant. The
// floor matters too: sleeping less than a full interval is what the
// empty-queue hot-spin regression was.
func (c *WorkerConf) pollSleep() {
	interval := c.CheckIntervalMs()
	c.sleepUnlessStopping(interval + httpx.FullJitter(interval/4, interval/4, 0))
}

// backoffSleep waits out a full-jitter backoff — rand(0, min(interval<<n,
// max)) — after a failed iteration. Full jitter rather than a fixed
// interval because the failure this is built for is a server restart, which
// every worker on the box sees at the same moment; retrying in lockstep
// afterwards just moves the thundering herd. This doubles as the "jitter
// the first poll after a reconnect" rule, since the sleep before the
// successful poll is the last of these.
func (c *WorkerConf) backoffSleep(attempt int) time.Duration {
	d := httpx.FullJitter(c.CheckIntervalMs(), timing.ScaleSeconds(MAX_POLL_BACKOFF_SECONDS), attempt)
	c.sleepUnlessStopping(d)
	return d
}

// sleepUnlessStopping sleeps for d, but wakes within one check interval if
// a shutdown signal arrives. Without this, a worker backing off against a
// dead server could sit unresponsive to SIGTERM for the length of the
// backoff.
func (c *WorkerConf) sleepUnlessStopping(d time.Duration) {
	chunk := c.CheckIntervalMs()
	if chunk <= 0 {
		chunk = 100 * time.Millisecond
	}
	deadline := time.Now().Add(d)
	for {
		if c.stopRequested() {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		if remaining > chunk {
			remaining = chunk
		}
		time.Sleep(remaining)
	}
}

// ProcessTasks is the worker's main loop: refresh state, claim a task, run
// it, repeat — until the worker is marked Stopped (typically by the SIGTERM
// handler updating the DB record). Sleeps c.CheckIntervalMs() whenever an
// iteration ends without processing a task (empty queue, refresh error,
// claim error). Returns the last error seen, or nil on clean shutdown.
//
// FIXME: Once working on a task, send some logs of errors into that task's logfiles
func (c *WorkerConf) ProcessTasks() error {
	var lastErr error
	var t tasks.Task

	// outageAttempts counts consecutive failed iterations, and drives the
	// full-jitter backoff. Reset on any successful iteration.
	outageAttempts := 0

	for !c.Stopped && !c.stopRequested() {
		// Update the worker config
		err := c.Refetch()
		if err != nil {
			delay := c.backoffSleep(outageAttempts)
			outageAttempts++
			log.WithFields(log.Fields{
				"id":         c.Id,
				"error":      err.Error(),
				"nattempts":  outageAttempts,
				"retryDelay": delay,
			}).Error("error refreshing worker state")
			lastErr = err
			continue
		}
		outageAttempts = 0
		log.WithFields(log.Fields{
			"id": c.Id,
		}).Info("successfully refreshed worker state")

		t, err = tasks.MarkAsClaimed(c.Id)
		if err != nil {
			delay := c.backoffSleep(outageAttempts)
			outageAttempts++
			log.WithFields(log.Fields{
				"err":        err.Error(),
				"nattempts":  outageAttempts,
				"retryDelay": delay,
			}).Errorf("error finding task for this worker")
			lastErr = err
			continue
		}
		if t.Id.IsZero() {
			// Empty queue — back off before polling again. (Pre-fix this
			// branch fell through with no sleep, hot-spinning the loop.)
			log.WithFields(log.Fields{
				"retryDelay": c.CheckIntervalMs(),
			}).Debug("found no matching tasks")
			c.pollSleep()
			continue
		}

		// FIXME: This is producing invalid JSON
		log.WithFields(log.Fields{
			"task": t,
		}).Info("Found task to process")

		err = c.ProcessOne(&t)
		if err == nil {
			log.WithFields(log.Fields{
				"task": t,
			}).Infof("processed task successfully")
			lastErr = nil
		} else {
			log.WithFields(log.Fields{
				"err":        err.Error(),
				"retryDelay": c.CheckIntervalMs(),
			}).Errorf("error processing task")
			lastErr = err
		}
		// No sleep after a task — drain the queue if more is waiting.
	}

	log.WithFields(log.Fields{
		"stopped":       c.Stopped,
		"stopRequested": c.stopRequested(),
		"pid":           c.Pid,
		"id":            c.Id.Hex(),
	}).Info("Finished final task, shutting down")

	return lastErr
}

// ProcessOne runs a single claimed task to completion: start the child
// process, tell the server it's RUNNING, watch it, report the terminal
// state, and journal the outcome to disk along the way.
//
// Concurrency note (turtlemonvh/blanket#23 phase 1): the monitoring
// goroutine started below shares nothing mutable with this function. It
// used to refresh *t in place and assign to this function's `err` while
// cmd.Wait() was writing it, which was two data races and made the
// task's observed state depend on which goroutine polled last. It now
// works from its own copies.
func (c *WorkerConf) ProcessOne(t *tasks.Task) error {
	// FIXME: Copy template into result directory
	// Do this BEFORE reading to make sure we're reading the version we save

	tt, err := t.GetTaskType()
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to get task type information")
		return err
	}

	var cmd *exec.Cmd
	cmd, err = t.GetCmd(tt)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed to build the command for this task")
		return err
	}

	// Add extra environment variables common for all tasks
	extraEnv := map[string]string{
		"BLANKET_APP_TASK_ID":                t.Id.Hex(),
		"BLANKET_APP_RESULTS_DIRECTORY":      viper.GetString("tasks.resultsPath"),
		"BLANKET_APP_TASK_RESULTS_DIRECTORY": path.Join(viper.GetString("tasks.resultsPath"), t.Id.Hex()),
		"BLANKET_APP_WORKER_PID":             cast.ToString(c.Pid),
		"BLANKET_APP_SERVER_PORT":            viper.GetString("port"),
	}
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// The fencing token for this run, generated immediately before the
	// child starts and sent on every transition that follows. See
	// tasks.Task.RunId. It's exported to the child too, so a task script
	// reporting its own progress can identify the run it belongs to.
	runId := objectid.NewObjectId().Hex()
	taskId := t.Id
	resultDir := t.ResultDir
	cmd.Env = append(cmd.Env, fmt.Sprintf("BLANKET_APP_TASK_RUN_ID=%s", runId))

	var fileCloser func()
	err, fileCloser = c.SetupExecutionDirectory(t, tt, cmd)
	if err != nil {
		return err
	}
	defer fileCloser()

	err = cmd.Start()
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("Error starting task execution")
		terr := tasks.MarkAsFinished(t, "ERROR", runId)
		if terr != nil {
			log.WithFields(log.Fields{
				"err":    terr.Error(),
				"taskId": t.Id,
			}).Error("After failing to start task execution, failed to transition task to state ERROR")
			return terr
		}
		return err
	}

	// The child exists now, so from here on there is something worth
	// recovering if this worker dies. Write the journal before telling the
	// server anything: a crash between Start and the RUNNING transition is
	// otherwise completely invisible.
	journal := &OutcomeJournal{
		State:     OutcomeStateRunning,
		RunId:     runId,
		TaskId:    taskId.Hex(),
		WorkerId:  c.Id.Hex(),
		Pid:       cmd.Process.Pid,
		StartedTs: time.Now().Unix(),
	}
	c.writeJournal(resultDir, journal)

	// FIXME: Move more fields here
	err = tasks.MarkAsRunning(t, runId, map[string]string{
		"timeout":    tt.Config.GetString("timeout"),
		"pid":        cast.ToString(cmd.Process.Pid),
		"typeDigest": tt.ConfigVersionHash,
	})
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed to transition task to state RUNNING")
		return err
	}

	// Pull the server-assigned StartedTs/Timeout back so the deadline below
	// matches the one the server recorded. Fail open if the server can't be
	// reached: fall back to the task type's own timeout measured from now,
	// rather than treating an unreachable server as a reason to compute a
	// deadline of zero and kill the task instantly.
	maxTime := time.Now().Unix() + cast.ToInt64(tt.Config.GetString("timeout"))
	if rerr := t.Refresh(); rerr != nil {
		log.WithFields(log.Fields{
			"err":     rerr.Error(),
			"taskId":  taskId,
			"maxTime": maxTime,
		}).Warn("could not refresh task after marking it RUNNING; using locally computed timeout")
	} else if t.Timeout > 0 {
		maxTime = t.StartedTs + t.Timeout
	}

	// taskDone tells the monitoring goroutine to exit; monitorDone reports
	// that it has. ProcessOne does not return until the goroutine is gone:
	// it is the only other user of this task's state, and letting it
	// outlive the call means "the task is finished" isn't actually true
	// yet. (It also leaves a goroutine reading process-global config while
	// the next caller writes it, which the race detector correctly
	// objects to.) The wait is bounded by the goroutine's own HTTP
	// timeouts.
	taskDone := make(chan struct{})
	monitorDone := make(chan struct{})
	stopMonitor := sync.OnceFunc(func() { close(taskDone) })
	defer func() {
		stopMonitor()
		<-monitorDone
	}()

	taskTimeout := time.NewTimer(timing.ScaleSeconds(float64(maxTime - time.Now().Unix())))
	go func() {
		defer close(monitorDone)
		// Everything below is either a local or a value captured before
		// this goroutine started. In particular it refreshes its own copy
		// of the task rather than the caller's.
		snapshot := tasks.Task{Id: taskId}
		stdout, _ := cmd.Stdout.(*os.File)
		stderr, _ := cmd.Stderr.(*os.File)

		for {
			log.WithFields(log.Fields{
				"taskId":  taskId,
				"maxTime": maxTime,
			}).Debug("looping in task process monitoring thread")

			// Check that we haven't stopped this task from another process.
			//
			// Fail open on an error (turtlemonvh/blanket#23 phase 1): an
			// unreachable server must never cause running work to be
			// killed. Before, the error was dropped on the floor and
			// snapshot.State kept whatever it had, which happened to be
			// safe but only by accident — and said nothing in the logs.
			if rerr := snapshot.Refresh(); rerr != nil {
				log.WithFields(log.Fields{
					"err":    rerr.Error(),
					"taskId": taskId,
				}).Warn("could not refresh task state; leaving the task running and retrying")
			} else if snapshot.State == "STOPPED" {
				log.WithFields(log.Fields{
					"taskId": taskId,
					"pid":    cmd.Process.Pid,
				}).Warn("killing task because state is STOPPED")
				cmd.Process.Kill()
				return
			}

			// Flush log files
			if stdout != nil {
				stdout.Sync()
			}
			if stderr != nil {
				stderr.Sync()
			}
			log.WithFields(log.Fields{
				"taskId": taskId,
			}).Debug("Flushing logfiles for task")

			// Either wait for next loop or exit
			loopTimeout := time.NewTimer(c.CheckIntervalMs())
			select {
			case killTime := <-taskTimeout.C:
				// Ran out of time. Report first, then kill regardless of
				// whether the report landed — a task that has blown its
				// deadline must not keep running just because the server
				// was unreachable. TimeoutFinishDeadline keeps that report
				// short for the same reason.
				loopTimeout.Stop()
				if merr := tasks.MarkAsFinishedWithin(&snapshot, "TIMEDOUT", runId, tasks.TimeoutFinishDeadline); merr != nil {
					log.WithFields(log.Fields{
						"err":    merr.Error(),
						"taskId": taskId,
					}).Error("failed to transition task to state TIMEDOUT")
				}
				log.WithFields(log.Fields{
					"taskId":   taskId,
					"maxTime":  maxTime,
					"killTime": killTime,
				}).Error("killing task because over max time allowed for execution")
				cmd.Process.Kill()
				return
			case <-loopTimeout.C:
				// Loop again
				continue
			case <-taskDone:
				loopTimeout.Stop()
				taskTimeout.Stop()
				return
			}
		}
	}()

	waitErr := cmd.Wait()

	// The child is gone; stop the monitor and wait for it to finish before
	// reporting, so it can't race a TIMEDOUT in on top of the real outcome.
	stopMonitor()
	<-monitorDone

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	journal.State = OutcomeStateExited
	journal.ExitedTs = time.Now().Unix()
	journal.ExitCode = exitCode
	c.writeJournal(resultDir, journal)

	finalState := "SUCCESS"
	if waitErr != nil {
		finalState = "ERROR"
		log.WithFields(log.Fields{
			"err":      waitErr.Error(),
			"exitCode": exitCode,
			"taskId":   taskId,
		}).Error("problems finishing task execution")
	}

	ferr := tasks.MarkAsFinished(t, finalState, runId)
	if ferr != nil {
		// The outcome is on disk and stays there: the server never
		// acknowledged it, so the phase 3 reaper is the one that will
		// recover this task.
		log.WithFields(log.Fields{
			"err":    ferr.Error(),
			"state":  finalState,
			"taskId": taskId,
		}).Error("failed to transition task to its terminal state; leaving the outcome journal in place for recovery")
		return ferr
	}

	// Acknowledged. Mark the journal reported before unlinking it, so a
	// crash in this window leaves a file that explains itself rather than
	// one that looks like unreported work.
	journal.State = OutcomeStateReported
	c.writeJournal(resultDir, journal)
	if rerr := RemoveOutcomeJournal(resultDir); rerr != nil {
		log.WithFields(log.Fields{
			"err":    rerr.Error(),
			"taskId": taskId,
		}).Warn("failed to remove outcome journal after a successful finish")
	}

	return waitErr
}

// writeJournal writes the outcome journal, failing open. Nothing in the
// running path depends on the journal: it exists for a reaper that may
// never need to read it, so a full disk or a read-only result directory
// must degrade recovery, not break the task.
func (c *WorkerConf) writeJournal(resultDir string, j *OutcomeJournal) {
	if err := WriteOutcomeJournal(resultDir, j); err != nil {
		log.WithFields(log.Fields{
			"err":       err.Error(),
			"taskId":    j.TaskId,
			"state":     j.State,
			"resultDir": resultDir,
		}).Warn("failed to write task outcome journal; a crash from here on will not be recoverable")
	}
}

// Create the execution directory for a task
// Includes attaching log files to the cmd object
func (c *WorkerConf) SetupExecutionDirectory(t *tasks.Task, tt *tasks.TaskType, cmd *exec.Cmd) (error, func()) {
	// Set up output files and configure the task to run in the correct location
	err := os.MkdirAll(t.ResultDir, os.ModePerm)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed to create scratch directory for task")
		return err, func() {}
	}

	// FIXME: Can set to the same file to get golang to combine streams
	// https://golang.org/pkg/os/exec/#Cmd
	stdoutPath := path.Join(t.ResultDir, fmt.Sprintf("blanket.stdout.log"))
	stderrPath := path.Join(t.ResultDir, fmt.Sprintf("blanket.stderr.log"))
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed to create stdout file for task")
		return err, func() {}
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed to create stderr file for task")
		return err, func() {
			stdoutFile.Close()
		}
	}

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	cmd.Dir = t.ResultDir

	fileCloser := func() {
		stdoutFile.Close()
		stderrFile.Close()
	}

	// The copier should use the location of the task type as its starting point
	// for relative path searches for files
	fileCopier := lib.FileCopier{
		BasePath: path.Dir(tt.ConfigFile),
	}

	filesToInclude := lib.ToSliceStringSlice(tt.Config.Get("files_to_include"))
	err = fileCopier.CopyFiles(filesToInclude, t.ResultDir)
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed copy files for task")
		return err, fileCloser
	} else {
		log.WithFields(log.Fields{
			"files":  filesToInclude,
			"taskId": t.Id,
		}).Error("copied files for task")
	}

	return err, fileCloser
}
