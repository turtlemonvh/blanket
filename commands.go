/*

Imports the commands folder
Directs to relevant command line option

*/

package main

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/turtlemonvh/blanket/command"
)

func GetMainCommand() *cobra.Command {

	var BlanketCmd = &cobra.Command{
		Use:   "blanket",
		Short: "Blanket is a RESTy wrapper for other programs",
		Long: `A fast and easy way to wrap applications and make 
               them available via nice clean REST interfaces with 
               built in UI, command line tools, and queuing, all 
               in a single binary!`,
		Run: func(cmd *cobra.Command, args []string) {
			command.Serve()
		},
	}

	var versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number of blanket",
		Long:  `All software has versions. This is blanket's`,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("blanket v0.1")
		},
	}

	var workerConf command.WorkerConf
	var workerCmd = &cobra.Command{
		Use:   "worker",
		Short: "Run a worker with capabilities defined by tags",
		Run: func(cmd *cobra.Command, args []string) {
			workerConf.RunWorker()
		},
	}
	workerCmd.Flags().StringVarP(&workerConf.Tags, "tags", "t", "", "Tags defining capabilities of this worker")
	workerCmd.Flags().StringVar(&workerConf.Logfile, "logfile", "", "Logfile to use")
	workerCmd.Flags().BoolVarP(&workerConf.Daemon, "daemon", "d", false, "Run as a daemon")

	var helloCmd = &cobra.Command{
		Use:   "hello",
		Short: "Say Hi",
		Run: func(cmd *cobra.Command, args []string) {
			command.Hello()
		},
	}

	BlanketCmd.AddCommand(workerCmd)
	BlanketCmd.AddCommand(versionCmd)
	BlanketCmd.AddCommand(helloCmd)

	return BlanketCmd
}
