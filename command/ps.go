package command

import (
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	All          bool
	Quiet        bool
	State        string
	Type         string
	RequiredTags string
	MaxTags      string
	Limit        int
	ParsedTags   []string
}

func init() {
	// Add options for tags, state, and view template
	psCmd.Flags().BoolVarP(&psConf.All, "all", "a", false, "Print tasks in all states")
	psCmd.Flags().StringVarP(&psConf.State, "state", "s", "RUNNING", "Only list tasks in this state")
	psCmd.Flags().StringVarP(&psConf.Type, "type", "t", "", "Only list tasks of this type")
	psCmd.Flags().StringVar(&psConf.RequiredTags, "requiredTags", "", "Only list tasks whose tags are a superset of these tags (comma separated)")
	psCmd.Flags().StringVar(&psConf.MaxTags, "maxTags", "", "Only list tasks whose tags are a subset of these tags (comma separated)")
	psCmd.Flags().IntVar(&psConf.Limit, "limit", 500, "Maximum number of items to return")
	psCmd.Flags().BoolVarP(&psConf.Quiet, "quiet", "q", false, "Print ids only")
	RootCmd.AddCommand(psCmd)
}

func (c *PSConf) ListTasks() {
	v := url.Values{}
	if c.State != "" && !c.All {
		v.Set("states", strings.ToUpper(c.State))
	}
	if c.Type != "" {
		v.Set("types", c.Type)
	}
	if c.RequiredTags != "" {
		v.Set("requiredTags", c.RequiredTags)
	}
	if c.MaxTags != "" {
		v.Set("maxTags", c.MaxTags)
	}
	v.Set("limit", strconv.Itoa(c.Limit))

	paramsString := v.Encode()
	reqURL := fmt.Sprintf("http://localhost:%d/task/", viper.GetInt("port"))
	if paramsString != "" {
		reqURL += "?" + paramsString
	}
	res, err := http.Get(reqURL)
	if err != nil {
		log.Fatalf(err.Error())
	}

	defer res.Body.Close()

	var tasks []map[string]interface{}
	dec := json.NewDecoder(res.Body)
	dec.Decode(&tasks)

	templateString := "{{.id}} {{.type}} {{.state}} {{.tags}} \n"
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

	if !c.Quiet && len(tasks) != 0 {
		tmpl.Execute(os.Stdout, headerRow)
	}
	for _, t := range tasks {
		tmpl.Execute(os.Stdout, t)
	}
}
