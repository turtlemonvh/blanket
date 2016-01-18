package command

/*
Run as a daemon
https://github.com/takama/daemon
*/

import (
	log "github.com/Sirupsen/logrus"
	"github.com/kardianos/osext"
	"github.com/spf13/cobra"
	_ "os/exec"
	"strings"
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
	}
}

func (c *WorkerConf) GetTask() {
	// Call the REST api and get a task with the required tags
}
