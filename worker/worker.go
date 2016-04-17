package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/kardianos/osext"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/lib"
	"github.com/turtlemonvh/blanket/tasks"
	"gopkg.in/mgo.v2/bson"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"text/template"
	"time"
)

const (
	DEFAULT_CHECK_INTERVAL_SECONDS = 2
)

// Worker

// CLean up id and parsed tags' parse these in cli

type WorkerConf struct {
	Id            bson.ObjectId  `json:"id"`
	Tags          []string       `json:"tags"`
	Logfile       string         `json:"logfile"`
	Daemon        bool           `json:"daemon"`
	Pid           int            `json:"pid"`
	Stopped       bool           `json:"stopped"`
	CheckInterval float64        `json:"checkInterval"` // seconds
	StartedTs     int64          `json:"startedTs"`
	fileCopier    lib.FileCopier // FIXME: This does not need to be here at all
}

// FIXME: Ensure this works ok on windows: https://golang.org/pkg/os/#Signal
// FIXME: Handle Ctrl-C; should try to deregister
// FIXME: Handle SIGHUP by updating information on dashboard (report, refresh config)
// FIXME: Modify global log object
// FIXME: Make sure logging works fine with sighup for logrotate
// https://en.wikipedia.org/wiki/Unix_signal#POSIX_signals
func (c *WorkerConf) Run() error {
	var err error

	// Initialize
	c.StartedTs = time.Now().Unix()
	if c.Id == "" {
		// Allow users to pass in existing ids to re-use old worker configs
		c.Id = bson.NewObjectId()
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

		cmd := exec.Command(path, "worker")
		if len(c.Tags) != 0 {
			cmd.Args = append(cmd.Args, "--tags")
			cmd.Args = append(cmd.Args, strings.Join(c.Tags, ","))
		}
		if c.Id != "" {
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

		if cmd.SysProcAttr != nil {
			cmd.SysProcAttr.Setpgid = true
		} else {
			cmdAttrs := &syscall.SysProcAttr{}
			cmdAttrs.Setpgid = true
			cmd.SysProcAttr = cmdAttrs
		}

		// FIXME: Redirect the first couple seconds of stdout here to check that process started ok
		cmd.Start()

		log.WithFields(log.Fields{
			"tags":          c.Tags,
			"pid":           cmd.Process.Pid,
			"checkInterval": c.CheckInterval,
			"logfile":       c.Logfile,
		}).Info("Starting daemonized executable")

	} else {
		// Calculate adjusted check time, in seconds
		if c.CheckInterval < 0.5 {
			c.CheckInterval = 0.5
		}

		// Handle clean shutdown
		shutdownChan := make(chan os.Signal, 1)
		signal.Notify(shutdownChan, os.Interrupt)
		signal.Notify(shutdownChan, syscall.SIGTERM)
		go func() {
			<-shutdownChan
			// Send a request to /worker/<id>/shutdown?nosignal to make sure db is updated with state
			// If the shutdown request initiated from the outside this won't update anything
			maxShutdownRetries := 10
			nShutdownRetries := 0
			log.Warn("Received shutdown signal; attempting to put worker in stopping state")
			err = c.Stop()
			for err != nil && nShutdownRetries < maxShutdownRetries {
				log.WithFields(log.Fields{
					"err": err.Error(),
				}).Fatal("problem updating worker to the 'stopped' state")
				log.Info("Retrying again in 1 second")
				time.Sleep(c.CheckIntervalMs())
			}
			if nShutdownRetries < maxShutdownRetries {
				log.Info("Successfully registered worker as 'stopped'.")
			} else {
				// Worker will exit when it checks its 'stopped' setting
				log.Infof("Failed to register worker as 'stopped' after %d attempts. Exiting anyway.", nShutdownRetries)
			}
		}()

		c.Pid = os.Getpid()

		err = c.SetLogfileName()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Fatal("problem getting logfile name")
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
		c.ProcessTasks()
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

func (c *WorkerConf) Stop() error {
	var err error

	reqURL := fmt.Sprintf("http://localhost:%d/worker/%s/stop", viper.GetInt("port"), c.Id.Hex())
	req, err := http.NewRequest("PUT", reqURL, nil)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	return err
}

func (c *WorkerConf) UpdateInDatabase() error {
	var err error
	var bts []byte

	bts, err = json.Marshal(c)
	if err != nil {
		return err
	}

	reqURL := fmt.Sprintf("http://localhost:%d/worker/%s", viper.GetInt("port"), c.Id.Hex())
	req, err := http.NewRequest("PUT", reqURL, bytes.NewReader(bts))
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	return err
}

func (c *WorkerConf) Refetch() error {
	reqURL := fmt.Sprintf("http://localhost:%d/worker/%s", viper.GetInt("port"), c.Id.Hex())
	res, err := http.Get(reqURL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	dec := json.NewDecoder(res.Body)
	return dec.Decode(c)
}

func (c *WorkerConf) CheckIntervalMs() time.Duration {
	return time.Duration(c.CheckInterval*1000*viper.GetFloat64("timeMultiplier")) * time.Millisecond
}

// FIXME: Remove Stop / Shutdown

// FIXME: Once working on a task, send logs of errors into its logfiles
func (c *WorkerConf) ProcessTasks() {
	var err error
	var t tasks.Task

	for !c.Stopped {
		if err != nil {
			// Only pause when we had a problem
			time.Sleep(c.CheckIntervalMs())
		}

		// Update the worker config
		err = c.Refetch()
		if err != nil {
			log.WithFields(log.Fields{
				"error": err.Error(),
			}).Error("error refreshing worker state")
		} else {
			log.WithFields(log.Fields{
				"id": c.Id,
			}).Info("successfully refreshed worker state")
		}

		// FIXME: Handle task not found differently than a 500, 401, etc
		t, err = tasks.MarkAsClaimed(c.Id)
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Errorf("could not find task; trying again in %d milliseconds", c.CheckIntervalMs())
			continue
		} else if !t.Id.Valid() {
			// FIXME: Make sure this works; should work because will initialize with empty string
			log.Debugf("found no matching tasks; trying again in %d milliseconds", c.CheckIntervalMs())
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
		} else {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Errorf("error processing task; trying again in %d milliseconds", c.CheckIntervalMs())
		}
	}

	log.WithFields(log.Fields{
		"stopped": c.Stopped,
		"pid":     c.Pid,
		"id":      c.Id.Hex(),
	}).Info("Finished final task, shutting down")
	if err != nil {
		os.Exit(1)
	} else {
		os.Exit(0)
	}
}

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
		}).Error("problems starting task execution")
		terr := tasks.MarkAsFinished(t, "ERROR")
		if terr != nil {
			log.WithFields(log.Fields{
				"err":    terr.Error(),
				"taskId": t.Id,
			}).Error("after failing to start task execution, failed to transition task to state ERROR")
			return terr
		}
		return err
	}

	// FIXME: Move more fields here
	err = tasks.MarkAsRunning(t, map[string]string{
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
	t.Refresh()

	taskDone := make(chan struct{})
	maxTime := t.StartedTs + t.Timeout
	go func() {
		// FIXME: Seems to behave strange when parent function has exited
		for true {
			log.WithFields(log.Fields{
				"taskId":       t.Id,
				"maxTime":      maxTime,
				"processState": cmd.ProcessState,
			}).Debug("looping in task process monitoring thread")

			// Check if started
			if cmd.Process == nil {
				// Either wait a bit for the command to start or exit
				select {
				case <-time.After(time.Duration(2*viper.GetFloat64("timeMultiplier")) * time.Second):
					continue
				case <-taskDone:
					return
				}
			}

			// Check if still running
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				log.WithFields(log.Fields{
					"taskId": t.Id,
				}).Info("returning from task process monitoring thread because task has exited")
				return
			}

			// Check that we're not over time
			checkTime := time.Now().Unix()
			if checkTime > maxTime {
				err = tasks.MarkAsFinished(t, "TIMEDOUT")
				if err != nil {
					log.WithFields(log.Fields{
						"err":    err.Error(),
						"taskId": t.Id,
					}).Error("failed to transition task to state TIMEDOUT")
				} else {
					log.WithFields(log.Fields{
						"taskId":    t.Id,
						"maxTime":   maxTime,
						"checkTime": checkTime,
					}).Error("killing task because over max time allowed for execution")
					if cmd.Process != nil {
						cmd.Process.Kill()
						return
					}
				}
			}

			// Check that we haven't stopped this task from another process
			t.Refresh()
			if t.State == "STOPPED" {
				if cmd.Process != nil {
					log.WithFields(log.Fields{
						"taskId": t.Id,
						"pid":    cmd.Process.Pid,
					}).Warn("killing task because state is STOPPED")
					cmd.Process.Kill()
					return
				}
			}

			// Flush log files
			cmd.Stdout.(*os.File).Sync()
			cmd.Stderr.(*os.File).Sync()
			log.WithFields(log.Fields{
				"taskId": t.Id,
			}).Debug("Flushing logfiles for task")

			// Either wait for next loop or exit
			// FIXME: Clean up with 2 timers
			select {
			case <-time.After(time.Duration(math.Min(float64(maxTime-checkTime), c.CheckInterval)*1000*viper.GetFloat64("timeMultiplier")) * time.Millisecond):
				continue
			case <-taskDone:
				return
			}
		}
	}()

	err = cmd.Wait()
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("problems finishing task execution")
		close(taskDone)
		terr := tasks.MarkAsFinished(t, "ERROR")
		if terr != nil {
			log.WithFields(log.Fields{
				"err":    terr.Error(),
				"taskId": t.Id,
			}).Error("after failing to finish task execution, failed to transition task to state ERROR")
			return terr
		}
		return err
	}

	err = tasks.MarkAsFinished(t, "SUCCESS")
	if err != nil {
		log.WithFields(log.Fields{
			"err":    err.Error(),
			"taskId": t.Id,
		}).Error("failed to transition task to state SUCCESS")
		close(taskDone)
	}

	return err
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
	c.fileCopier = lib.FileCopier{
		BasePath: path.Dir(tt.ConfigFile),
	}

	filesToInclude := lib.ToSliceStringSlice(tt.Config.Get("files_to_include"))
	err = c.fileCopier.CopyFiles(filesToInclude, t.ResultDir)
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
