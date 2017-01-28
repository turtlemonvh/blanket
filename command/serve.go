package command

import (
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
		defer db.Close()

		// DB and Q initializers are fatal if they don't succeed
		// Serve gracefully

		c := server.ServerConfig{
			DB:             bolt.NewBlanketBoltDB(db),
			Q:              bolt.NewBlanketBoltQueue(db),
			Port:           viper.GetInt("port"),
			ResultsPath:    viper.GetString("tasks.resultsPath"),
			TimeMultiplier: viper.GetFloat64("timeMultiplier"),
		}
		s := c.Serve()
		s.ListenAndServe()
	},
}
