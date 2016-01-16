package command

import (
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"io/ioutil"
	"log"
	"net/http"
)

/*

Like `docker ps`

Lists running tasks and queued tasks


*/

var psConf PSConf
var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List active and queued tasks",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		psConf.ListTasks()
	},
}

type PSConf struct {
	All bool
}

func init() {
	psCmd.Flags().BoolVarP(&psConf.All, "all", "a", false, "Print an extended list")
	RootCmd.AddCommand(psCmd)
}

func (c *PSConf) ListTasks() {
	// Use RootConfig to decide what port to hit
	res, err := http.Get(fmt.Sprintf("http://localhost:%d/task/", viper.GetInt("port")))
	if err != nil {
		log.Fatalf(err.Error())
	}

	defer res.Body.Close()
	tasks, err := ioutil.ReadAll(res.Body)
	fmt.Printf(string(tasks))
}
