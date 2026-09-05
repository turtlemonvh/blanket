package command

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	bolt "github.com/turtlemonvh/blanket/lib/bolt"
	"github.com/turtlemonvh/blanket/server"
)

var serverLongDesc string = `A fast and easy way to wrap applications and make them available via nice clean REST interfaces with built in UI, command line tools, and queuing, all in a single binary!`
var RootCmd = &cobra.Command{
	Use:   "blanket",
	Short: "Blanket is a RESTy wrapper for other programs",
	Long:  serverLongDesc,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		InitializeLogging()

		// Connect to database
		db := bolt.MustOpenBoltDatabase()
		// Belt and braces: the server closes this itself as the last step
		// of its shutdown sequence (ServerConfig.Cleanup below), which is
		// what releases the bolt lock before a SIGUSR2 re-exec. bbolt's
		// Close is a no-op on an already-closed handle, so this defer only
		// covers paths that never reach the server at all.
		defer db.Close()

		// DB and Q initializers are fatal if they don't succeed
		// Serve gracefully

		c := server.ServerConfig{
			DB:                    bolt.NewBlanketBoltDB(db),
			Q:                     bolt.NewBlanketBoltQueue(db),
			Port:                  viper.GetInt("port"),
			ResultsPath:           viper.GetString("tasks.resultsPath"),
			TimeMultiplier:        viper.GetFloat64("timeMultiplier"),
			Version:               Version,
			SchedulerInterval:     viper.GetDuration("scheduler.interval"),
			SchedulerMaxScheduled: viper.GetInt("scheduler.maxScheduled"),
			Cleanup: func() {
				if err := db.Close(); err != nil {
					log.WithField("err", err).Warn("error closing database at shutdown")
				}
			},
		}

		// Blocks until SIGINT/SIGTERM (drain, then exit leaving no restart
		// intent) or, on unix, SIGUSR2 (drain, then re-exec in place --
		// never returns). See server/lifecycle.go.
		if err := c.Serve().ListenAndServe(); err != nil {
			log.WithField("err", err).Fatal("server exited with an error")
		}
	},
}
