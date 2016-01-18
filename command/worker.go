package command

/*
Run as a daemon
https://github.com/takama/daemon
*/

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/tasks"
	"math"
	"net/http"
	"net/url"
	_ "os/exec"
	"strings"
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

func (c *WorkerConf) LaunchWorker() {
	for {
		t, err := c.GetTask()
		if err == nil {
			fmt.Printf("SUCCESS :: found task :: %s | %s | %s \n", t.Id, t.TypeId, t.Tags)
		} else {
			fmt.Printf("ERROR :: could not find task :: %s \n", err.Error())
			fmt.Println("ERROR :: Trying again in 5 seconds")
		}

		// Wait a little while
		time.Sleep(5000 * time.Millisecond)
	}
}

func (c *WorkerConf) GetTask() (tasks.Task, error) {
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
