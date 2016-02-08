package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/kardianos/osext"
	"github.com/spf13/cast"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"
	"time"
)

type WorkerConf struct {
	Tags          string
	ParsedTags    []string
	Logfile       string
	Daemon        bool
	Pid           int
	Stopping      bool
	CheckInterval int
}

// FIXME: Handle Ctrl-C; should try to deregister
func (c *WorkerConf) Run() {
	var err error

	// Handle clean shutdown
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt)
	signal.Notify(shutdownChan, syscall.SIGTERM)
	go func() {
		<-shutdownChan
		// Send a request to /worker/<id>/shutdown?nosignal to make sure db is updated with state
		// If the shutdown request initiated from the outside this won't update anything
		c.Stopping = true
	}()

	// TODO:
	// Make sure logging works fine with sighup for logrotate

	if c.Daemon {
		path, err := osext.Executable()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Error("Problem getting executable path")
			return
		}

		log.WithFields(log.Fields{
			"path": path,
		}).Info("Path to current executable is")

		cmd := exec.Command(path, "worker")
		if c.Tags != "" {
			cmd.Args = append(cmd.Args, "--tags")
			cmd.Args = append(cmd.Args, c.Tags)
		}
		if c.Logfile != "" {
			cmd.Args = append(cmd.Args, "--logfile")
			cmd.Args = append(cmd.Args, c.Logfile)
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
		c.Pid = os.Getpid()
		c.ParsedTags = strings.Split(c.Tags, ",")

		// FIXME: Make this an option
		c.CheckInterval = 2

		err = c.SetLogfileName()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Fatal("problem getting logfile name")
		}

		log.WithFields(log.Fields{
			"tags":          c.ParsedTags,
			"pid":           c.Pid,
			"checkInterval": c.CheckInterval,
			"logfile":       c.Logfile,
		}).Info("Starting executable")

		c.MustRegister()
		c.ProcessTasks()
	}
}

func (c *WorkerConf) SetLogfileName() error {
	if c.Logfile != "" {
		return nil
	}

	tmpl, err := template.New("logfile").Parse(viper.GetString("workers.logfile_name_template"))
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
	c.Stopping = false
}

// Deregisters itself
// Send a DELETE request to /worker/<id>/ to make sure db is cleared
// Logs that request was succesful and is shutting down
func (c *WorkerConf) Shutdown() {
	log.Error("Shutting worker down cleanly")
	os.Exit(1)
}

// FIXME: Once working on a task, send logs of errors into its logfiles
func (c *WorkerConf) ProcessTasks() {
	for !c.Stopping {
		time.Sleep(time.Duration(c.CheckInterval*1000) * time.Millisecond)

		t, err := c.FindTask()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Errorf("could not find task; trying again in %d seconds", c.CheckInterval)
			continue
		} else if t.Id == "" {
			log.Debugf("found no matching tasks; trying again in %d seconds", c.CheckInterval)
			continue
		}

		log.WithFields(log.Fields{
			"taskId": t.Id,
			"typeId": t.TypeId,
			"tags":   t.Tags,
		}).Info("Found task to process")

		err = c.ProcessOne(&t)
		if err == nil {
			log.WithFields(log.Fields{
				"taskId": t.Id,
				"typeId": t.TypeId,
				"tags":   t.Tags,
			}).Infof("processed task successfully; checking for next task in %d seconds", c.CheckInterval)
		} else {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Errorf("error processing task; trying again in %d seconds", c.CheckInterval)
		}
	}
	c.Shutdown()
}

func toSliceStringSlice(i interface{}) [][]string {
	s := cast.ToSlice(i)
	var r [][]string
	for _, v := range s {
		r = append(r, cast.ToStringSlice(v))
	}
	return r
}

func (c *WorkerConf) ProcessOne(t *tasks.Task) error {
	// Fetch information about the task type
	ttFilepath := path.Join(viper.GetString("tasks.types_path"), fmt.Sprintf("%s.toml", t.TypeId))
	tt, err := tasks.ReadTaskTypeFromFilepath(ttFilepath)
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to get task type information")
		return err
	}

	// Try to lock the task for editing
	err = c.TransitionTaskState(t, "START", map[string]string{"typeDigest": tt.ConfigVersionHash})
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to transition task to state START")
		return err
	}

	// Evaluate template
	tmpl, err := template.New("tasks").Parse(tt.Config.GetString("command"))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("problem parsing 'command' parameter as go template")
		return err
	}
	var cmdString bytes.Buffer
	err = tmpl.Execute(&cmdString, t.ExecEnv)
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("error evaluating template for command")
		return err
	}

	// FIXME: Don't just use bash; use python, zsh, etc configured via viper
	cmd := exec.Command("bash", "-c", cmdString.String())
	err = SetupExecutionDirectory(t, &tt, cmd)
	if err != nil {
		return err
	}

	// Modify execution environment with env variables
	// e.g. http://craigwickesser.com/2015/02/golang-cmd-with-custom-environment/
	env := os.Environ()
	for k, v := range t.ExecEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Env = env

	err = c.TransitionTaskState(t, "RUNNING", make(map[string]string))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to transition task to state RUNNING")
		return err
	}

	err = cmd.Start()
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("problems starting task execution")
		terr := c.TransitionTaskState(t, "ERROR", make(map[string]string))
		if terr != nil {
			log.WithFields(log.Fields{
				"err": terr.Error(),
			}).Error("after failing to start task execution, failed to transition task to state ERROR")
			return terr
		}
		return err
	}

	err = cmd.Wait()
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("problems finishing task execution")
		terr := c.TransitionTaskState(t, "ERROR", make(map[string]string))
		if terr != nil {
			log.WithFields(log.Fields{
				"err": terr.Error(),
			}).Error("after failing to finish task execution, failed to transition task to state ERROR")
			return terr
		}
		return err
	}

	err = c.TransitionTaskState(t, "SUCCESS", make(map[string]string))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to transition task to state SUCCESS")
	}

	return err
}

// FIXME: Return a function that closes file handles
// Can't use defer here because file will be closed before task executes
func SetupExecutionDirectory(t *tasks.Task, tt *tasks.TaskType, cmd *exec.Cmd) error {
	// Set up output files and configure the task to run in the correct location
	err := os.MkdirAll(t.ResultDir, os.ModePerm)
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to create scratch directory for task")
		return err
	}
	stdoutPath := path.Join(t.ResultDir, fmt.Sprintf("blanket.stdout.log"))
	stderrPath := path.Join(t.ResultDir, fmt.Sprintf("blanket.stderr.log"))
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to create stdout file for task")
		return err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed to create stderr file for task")
		return err
	}
	defer stderrFile.Close()

	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	cmd.Dir = t.ResultDir

	filesToInclude := toSliceStringSlice(tt.Config.Get("files_to_include"))
	err = CopyFiles(filesToInclude, t.ResultDir)
	if err != nil {
		log.WithFields(log.Fields{
			"err": err.Error(),
		}).Error("failed copy files for task")
		return err
	}

	return err
}

func CopyFiles(files [][]string, resultDir string) error {
	for icFile, cFile := range files {
		if len(cFile) < 1 {
			return fmt.Errorf("The array of file information for item %d in the list 'files_to_include' must have at least 1 component", icFile)
		}
		src := cFile[0]

		// Dest path is always relative
		dest := resultDir
		if len(cFile) > 1 {
			dest = path.Join(resultDir, cFile[1])

			// Create it if it doesn't exist
			err := os.MkdirAll(resultDir, os.ModePerm)
			if err != nil {
				return err
			}
		}

		// Clean up / source path
		// FIXME: May want to do this a different way by calling to the shell and running `cd <path>; pwd -P`
		// FIXME: Path expansion this way is wrong; '~' is different in '~/' and '/~a/'
		usr, err := user.Current()
		if err != nil {
			return err
		}
		src = strings.Replace(src, "~", usr.HomeDir, 1)
		if !filepath.IsAbs(src) {
			src = path.Join(viper.GetString("tasks.types_path"), src)
		}
		log.WithFields(log.Fields{"src": src, "dest": dest}).Info("Copying files from src to dest")

		var filesToCopy []string
		matches, err := filepath.Glob(src)
		if err != nil {
			return err
		}
		for _, fileMatch := range matches {
			// Check to make sure it is a file
			fileInfo, err := os.Stat(fileMatch)
			if err != nil {
				return err
			}

			if !fileInfo.IsDir() {
				filesToCopy = append(filesToCopy, fileMatch)
			} else {
				// Walk directory tree
				err = filepath.Walk(fileMatch, func(path string, f os.FileInfo, err error) error {
					if !f.IsDir() {
						filesToCopy = append(filesToCopy, path)
					}
					return nil
				})
				if err != nil {
					return err
				}
			}

		}

		absSrc, _ := filepath.Abs(src)
		log.WithFields(log.Fields{
			"matches":     matches,
			"filesToCopy": filesToCopy,
			"src":         src,
			"absSrc":      absSrc,
		}).Debug("Stats before copying")

		// Actual copy function
		for _, fileToCopy := range filesToCopy {
			s, err := os.Open(fileToCopy)
			if err != nil {
				return err
			}
			defer s.Close()

			destFilepath := path.Join(dest, path.Base(fileToCopy))

			// Create path it if it doesn't exist
			err = os.MkdirAll(filepath.Dir(destFilepath), os.ModePerm)
			if err != nil {
				return err
			}

			log.WithFields(log.Fields{
				"src":  fileToCopy,
				"dest": destFilepath,
			}).Debug("Copying a single file from src to dest")

			d, err := os.Create(destFilepath)
			defer d.Close()
			if err != nil {
				return err
			}
			if _, err := io.Copy(d, s); err != nil {
				return err
			}
			d.Close()
		}
	}

	return nil
}

func (c *WorkerConf) TransitionTaskState(t *tasks.Task, state string, extraVars map[string]string) error {
	urlParams := url.Values{}
	urlParams.Set("state", state) // START, RUNNING, ERROR/SUCCESS
	for k, v := range extraVars {
		urlParams.Set(k, v)
	}
	paramsString := urlParams.Encode()
	reqURL := fmt.Sprintf("http://localhost:%d/task/%s/state", viper.GetInt("port"), t.Id) + "?" + paramsString
	req, err := http.NewRequest("PUT", reqURL, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return nil
}

func (c *WorkerConf) FindTask() (tasks.Task, error) {
	// Call the REST api and get a task with the required tags
	// The worker needs to make sure it has all the tags of whatever task it requests
	v := url.Values{}

	v.Set("state", "WAIT")
	v.Set("maxTags", c.Tags)
	v.Set("limit", "1")
	v.Set("reverseSort", "true") // oldest to newest

	paramsString := v.Encode()
	reqURL := fmt.Sprintf("http://localhost:%d/task/", viper.GetInt("port")) + "?" + paramsString
	res, err := http.Get(reqURL)
	if err != nil {
		return tasks.Task{}, err
	}
	defer res.Body.Close()

	// Handle response by looking for item with latest timestamp
	var respTasks []tasks.Task
	dec := json.NewDecoder(res.Body)
	dec.Decode(&respTasks)

	// FIXME: Handle empty results
	// Always sorted oldest to newest
	if len(respTasks) > 0 {
		return respTasks[0], nil
	}
	return tasks.Task{}, nil
}
