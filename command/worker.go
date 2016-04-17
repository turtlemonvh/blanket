package command

import (
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/turtlemonvh/blanket/worker"
	"gopkg.in/mgo.v2/bson"
	"strings"
)

/*
Run as a daemon
https://github.com/takama/daemon
*/

var workerId string
var workerRawTags string
var workerConf worker.WorkerConf
var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "Run a worker with capabilities defined by tags",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()

		workerConf.Tags = strings.Split(workerRawTags, ",")
		if workerId != "" {
			if !bson.IsObjectIdHex(workerId) {
				log.WithFields(log.Fields{
					"id": workerId,
				}).Fatal("The id passed for a worker must be a valid mongo id")
			}
			workerConf.Id = bson.ObjectIdHex(workerId)
		}

		log.WithFields(log.Fields{
			"workerConf": workerConf,
		}).Debug("About to start worker")

		err := workerConf.Run()
		if err != nil {
			log.WithFields(log.Fields{
				"err": err.Error(),
			}).Fatal("error starting worker")
		}
	},
}

func init() {
	workerCmd.Flags().StringVarP(&workerRawTags, "tags", "t", "", "Tags defining capabilities of this worker")
	workerCmd.Flags().StringVar(&workerId, "id", "", "Id of this worker")
	workerCmd.Flags().StringVar(&workerConf.Logfile, "logfile", "", "Logfile to use")
	workerCmd.Flags().Float64Var(&workerConf.CheckInterval, "checkinterval", 0, "Check interval in seconds")
	workerCmd.Flags().BoolVarP(&workerConf.Daemon, "daemon", "d", false, "Run as a daemon")
	RootCmd.AddCommand(workerCmd)
}
