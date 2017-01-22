package command

import (
	"github.com/spf13/cobra"
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
		s := server.Serve(bolt.NewBlanketBoltDB(db), bolt.NewBlanketBoltQueue(db))
		s.ListenAndServe()
	},
}
