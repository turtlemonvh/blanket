package command

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
)

var rmConf RmConf
var rmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Remove tasks",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		viper.Set("logLevel", "error")
		InitializeLogging()
		if len(args) < 1 {
			fmt.Println("ERROR: Missing required positional argument 'taskId'")
			cmd.Usage()
			os.Exit(1)
		}
		rmConf.RemoveTask(args[0])
	},
}

type RmConf struct {
	Force  bool
	TaskId string
}

// FIXME: Accept stdin
func init() {
	// Add options for tags, state, and view template
	rmCmd.Flags().BoolVarP(&rmConf.Force, "force", "f", false, "Force deletion of tasks (ignore errors and warnings)")
	RootCmd.AddCommand(rmCmd)
}

func (c *RmConf) RemoveTask(taskId string) {
	err := client.DeleteTask(taskId, viper.GetInt("port"))
	if err == nil {
		return
	}
	if c.Force {
		// --force means "ignore errors and warnings": don't fail the
		// process over a delete that didn't go through.
		return
	}
	printAPIError(err)
	os.Exit(1)
}
