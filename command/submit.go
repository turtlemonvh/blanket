package command

import (
	"encoding/json"
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
)

var submitConf SubmitConf
var execCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit a task to be executed.",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		viper.Set("logLevel", "error")
		InitializeLogging()
		SubmitTask()
	},
}

type SubmitConf struct {
	Type  string
	Env   string
	Quiet bool
}

func init() {
	execCmd.Flags().StringVarP(&submitConf.Type, "type", "t", "", "Run task of this type")
	execCmd.Flags().StringVarP(&submitConf.Env, "env", "e", "{}", "JSON string representing execution env for this task.")
	execCmd.Flags().BoolVarP(&submitConf.Quiet, "quiet", "q", false, "Print the task id only")
	RootCmd.AddCommand(execCmd)
}

// FIXME: Include ability to send files
func SubmitTask() {
	executionEnvironment := make(map[string]interface{})
	err := json.Unmarshal([]byte(submitConf.Env), &executionEnvironment)
	if err != nil {
		log.Fatal("Error interpreting environment as valid json")
	}

	t, err := client.SubmitTask(submitConf.Type, executionEnvironment, viper.GetInt("port"))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err,
		}).Fatal("Error submitting task")
	}
	if submitConf.Quiet {
		fmt.Println(t.Id.Hex())
	} else {
		fmt.Println(t.String())
	}
}
