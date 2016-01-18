package command

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"log"
	"net/http"
	"os"
	"text/template"
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
	All   bool
	Quiet bool
}

func init() {
	// Add options for tags, state, and view template
	psCmd.Flags().BoolVarP(&psConf.All, "all", "a", false, "Print both queued and completed tasks")
	psCmd.Flags().BoolVarP(&psConf.Quiet, "quiet", "q", false, "Print ids only")
	RootCmd.AddCommand(psCmd)
}

func (c *PSConf) ListTasks() {
	// Use RootConfig to decide what port to hit
	res, err := http.Get(fmt.Sprintf("http://localhost:%d/task/", viper.GetInt("port")))
	if err != nil {
		log.Fatalf(err.Error())
	}

	defer res.Body.Close()

	var tasks []map[string]interface{}
	dec := json.NewDecoder(res.Body)
	dec.Decode(&tasks)

	templateString := "{{.id}} {{.type}} {{.state}} \n"
	if c.Quiet {
		templateString = "{{.id}}\n"
	}
	tmpl, err := template.New("tasks").Parse(templateString)
	if err != nil {
		log.Fatalf(err.Error())
	}

	// FIXME: Clean up formatting to make fields the same size
	headerRow := map[string]interface{}{
		"id":            fmt.Sprintf("%-36s", "Id"),
		"createdTs":     "CreatedTs",
		"startedTs":     "StartedTs",
		"lastUpdatedTs": "LastUpdatedTs",
		"type":          "TypeId",
		"resultDir":     "ResultDir",
		"state":         "State",
		"progress":      "Progress",
		"defaultEnv":    "ExecEnv",
		"tags":          "Tags",
	}

	if !c.Quiet {
		tmpl.Execute(os.Stdout, headerRow)
	}
	for _, t := range tasks {
		tmpl.Execute(os.Stdout, t)
	}
}
