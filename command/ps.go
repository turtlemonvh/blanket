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
	"text/tabwriter"
	"text/template"
)

var psConf PSConf
var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List active and queued tasks",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		viper.Set("logLevel", "error")
		InitializeLogging()
		psConf.ListTasks()
	},
}

type PSConf struct {
	All          bool
	Quiet        bool
	States       string
	Types        string
	RequiredTags string
	MaxTags      string
	Template     string
	Limit        int
	ParsedTags   []string
}

func init() {
	// Add options for tags, state, and view template
	psCmd.Flags().StringVarP(&psConf.States, "state", "s", "", "Only list tasks in these states (comma separated).")
	psCmd.Flags().StringVarP(&psConf.Types, "types", "t", "", "Only list tasks of these types (comma separated)")
	psCmd.Flags().StringVar(&psConf.RequiredTags, "requiredTags", "", "Only list tasks whose tags are a superset of these tags (comma separated)")
	psCmd.Flags().StringVar(&psConf.MaxTags, "maxTags", "", "Only list tasks whose tags are a subset of these tags (comma separated)")
	psCmd.Flags().StringVar(&psConf.Template, "template", "", "The template to use for listing tasks")
	psCmd.Flags().IntVarP(&psConf.Limit, "limit", "l", 500, "Maximum number of items to return")
	psCmd.Flags().BoolVarP(&psConf.Quiet, "quiet", "q", false, "Print ids only")
	RootCmd.AddCommand(psCmd)
}

func (c *PSConf) ListTasks() {
	v := url.Values{}
	if c.States != "" {
		v.Set("states", strings.ToUpper(c.States))
	}
	if c.Types != "" {
		v.Set("types", c.Types)
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

	if c.Template == "" {
		c.Template = "{{.id}} {{.type}} {{.state}} {{.tags}}"
	}
	if c.Quiet {
		c.Template = "{{.id}}"
	}

	// Prepare template strings for tabwriter
	c.Template = strings.Replace(c.Template, " ", "\t", -1)
	if !strings.HasSuffix(c.Template, "\n") {
		c.Template += "\n"
	}

	tmpl, err := template.New("tasks").Parse(c.Template)
	if err != nil {
		log.Fatalf(err.Error())
	}

	// FIXME: Clean up formatting to make fields the same size
	headerRow := map[string]interface{}{
		"id":            "ID",
		"createdTs":     "CREATED_TS",
		"startedTs":     "STARTED_TS",
		"lastUpdatedTs": "LAST_UPDATED_TS",
		"type":          "TYPE",
		"resultDir":     "RESULT_DIR",
		"state":         "STATE",
		"progress":      "PROGRESS",
		"defaultEnv":    "ENV",
		"tags":          "TAGS",
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 8, 1, '\t', 0)

	if !c.Quiet && len(tasks) != 0 {
		tmpl.Execute(w, headerRow)
	}
	for _, t := range tasks {
		tmpl.Execute(w, t)
	}

	w.Flush()
}
