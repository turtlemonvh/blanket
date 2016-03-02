package command

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"log"
	"net/http"
	"os"
)

/*

Like `docker ps`

Lists
- running tasks
- queued tasks (with -a)

Other commands list
- task types
- workers and their status


*/

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

func init() {
	// Add options for tags, state, and view template
	rmCmd.Flags().BoolVarP(&rmConf.Force, "force", "f", false, "Force deletion of tasks (ignore errors and warnings)")
	RootCmd.AddCommand(rmCmd)
}

func (c *RmConf) RemoveTask(taskId string) {
	// Use RootConfig to decide what port to hit
	req, err := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:%d/task/%s", viper.GetInt("port"), taskId), nil)
	if err != nil {
		log.Fatalf(err.Error())
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf(err.Error())
	}
	defer resp.Body.Close()
}
