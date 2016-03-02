package command

import (
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/turtlemonvh/blanket/worker"
)

/*
Run as a daemon
https://github.com/takama/daemon
*/

var workerConf worker.WorkerConf
var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run a worker with capabilities defined by tags",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()
		err := workerConf.Run()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Fatal("error starting worker")
		}
	},
}

func init() {
	workerCmd.Flags().StringVarP(&workerConf.Tags, "tags", "t", "", "Tags defining capabilities of this worker")
	workerCmd.Flags().StringVar(&workerConf.Logfile, "logfile", "", "Logfile to use")
	workerCmd.Flags().Float64Var(&workerConf.CheckInterval, "checkinterval", 0, "Check interval in seconds")
	workerCmd.Flags().BoolVarP(&workerConf.Daemon, "daemon", "d", false, "Run as a daemon")
	RootCmd.AddCommand(workerCmd)
}
