package command

/*
Run as a daemon
https://github.com/takama/daemon
*/

import (
	"bytes"
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/kardianos/osext"
	"github.com/spf13/cast"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

var workerConf WorkerConf
var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run a worker with capabilities defined by tags",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		workerConf.RunWorker()
	},
}

func init() {
	workerCmd.Flags().StringVarP(&workerConf.Tags, "tags", "t", "", "Tags defining capabilities of this worker")
	workerCmd.Flags().StringVar(&workerConf.Logfile, "logfile", "", "Logfile to use")
	workerCmd.Flags().BoolVarP(&workerConf.Daemon, "daemon", "d", false, "Run as a daemon")
	RootCmd.AddCommand(workerCmd)
}

type WorkerConf struct {
	Tags       string
	ParsedTags []string
	Logfile    string
	Daemon     bool
}

func (c *WorkerConf) RunWorker() {
	c.ParsedTags = strings.Split(c.Tags, ",")
	log.Info("Running with tags: ", c.ParsedTags)
	log.Info("Daemon: ", c.Daemon)

	// If it's a daemon, call it again
	if c.Daemon {
		path, err := osext.Executable()
		if err != nil {
			log.Error("Problem getting executable path")
			return
		}

		log.Info("Path to current executable is: ", path)

		// Launch worker
		// - send in options to tell it to log to file with pid
		// https://golang.org/pkg/os/#Setenv
		// - needs to
		//cmd := exec.Command(path, "worker", "--logfile", "")
		//cmd.Start()
	} else {
		c.LaunchWorker()
	}
}

// FIXME: Once working on a task, send logs of errors into its logfiles
func (c *WorkerConf) LaunchWorker() {
	for {
		// Wait at the start of the loop so early exits wait
		time.Sleep(5000 * time.Millisecond)

		t, err := c.FindTask()
		if err != nil {
			fmt.Printf("ERROR :: could not find task :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		} else if t.Id == "" {
			fmt.Printf("WARNING :: found no matching tasks \n")
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}

		log.WithFields(log.Fields{
			"taskId": t.Id,
			"typeId": t.TypeId,
			"tags":   t.Tags,
		}).Info("SUCCESS: Found task")

		// Fetch information about the task type
		ttFilepath := path.Join(viper.GetString("tasks.types_path"), fmt.Sprintf("%s.toml", t.TypeId))
		tt, err := tasks.ReadTaskTypeFromFilepath(ttFilepath)
		if err != nil {
			fmt.Printf("ERROR :: failed to get task type information :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}

		// Try to lock the task for editing
		err = c.TransitionTaskState(t, "START", map[string]string{"typeDigest": tt.ConfigVersionHash})
		if err != nil {
			fmt.Printf("ERROR :: failed to transition task to state START :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}

		// Evaluate template
		tmpl, err := template.New("tasks").Parse(tt.Config.GetString("command"))
		if err != nil {
			fmt.Printf("ERROR :: problem parsing 'command' parameter as go template :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}
		var cmdString bytes.Buffer
		err = tmpl.Execute(&cmdString, t.ExecEnv)
		if err != nil {
			fmt.Printf("ERROR :: error evaluating template for command :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}
		cmd := exec.Command("bash", "-c", cmdString.String())

		// Set up output files and configure the task to run in the correct location
		err = os.MkdirAll(t.ResultDir, os.ModePerm)
		if err != nil {
			fmt.Printf("ERROR :: failed to create scratch directory for task :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}
		stdoutPath := path.Join(t.ResultDir, fmt.Sprintf("blanket.stdout.log"))
		stderrPath := path.Join(t.ResultDir, fmt.Sprintf("blanket.stderr.log"))
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			fmt.Printf("ERROR :: failed to create stdout file for task :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}
		defer stdoutFile.Close()
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			fmt.Printf("ERROR :: failed to create stderr file for task :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}
		defer stderrFile.Close()
		cmd.Stdout = stdoutFile
		cmd.Stderr = stderrFile
		cmd.Dir = t.ResultDir

		filesToInclude := toSliceStringSlice(tt.Config.Get("files_to_include"))
		err = CopyFiles(filesToInclude, t.ResultDir)
		if err != nil {
			fmt.Printf("ERROR :: failed copy files for task :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
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
			fmt.Printf("ERROR :: failed to transition task to state RUNNING :: %s \n", err.Error())
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}

		err = cmd.Start()
		if err != nil {
			fmt.Printf("ERROR :: problems starting task execution :: %s \n", err.Error())
			c.TransitionTaskState(t, "ERROR", make(map[string]string))
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}

		err = cmd.Wait()
		if err != nil {
			fmt.Printf("ERROR :: problems finishing task execution :: %s \n", err.Error())
			c.TransitionTaskState(t, "ERROR", make(map[string]string))
			fmt.Println("INFO :: Trying again in 5 seconds")
			continue
		}

		err = c.TransitionTaskState(t, "SUCCESS", make(map[string]string))
		fmt.Println("SUCCESS :: Ran task successfully")
		fmt.Println("INFO :: Proceeding with next task in 5 seconds")
	}
}

func toSliceStringSlice(i interface{}) [][]string {
	s := cast.ToSlice(i)
	var r [][]string
	for _, v := range s {
		r = append(r, cast.ToStringSlice(v))
	}
	return r
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

		/*
			absSrc, _ := filepath.Abs(src)
			log.WithFields(log.Fields{
				"matches":     matches,
				"filesToCopy": filesToCopy,
				"src":         src,
				"absSrc":      absSrc,
			}).Info("Stats before copying")
		*/

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

			/*
				log.WithFields(log.Fields{
					"src":  fileToCopy,
					"dest": destFilepath,
				}).Info("Copying a single file from src to dest")
			*/

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

func (c *WorkerConf) TransitionTaskState(t tasks.Task, state string, extraVars map[string]string) error {
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
	var latestTask tasks.Task
	lowestTimestamp := int64(math.MaxInt64)
	for _, task := range respTasks {
		if task.CreatedTs < lowestTimestamp {
			latestTask = task
		}
	}

	return latestTask, nil
}
