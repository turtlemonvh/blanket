package command

/*
Run as a daemon
https://github.com/takama/daemon
*/

import (
	log "github.com/Sirupsen/logrus"
	"github.com/kardianos/osext"
	_ "os/exec"
	"strings"
)

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
