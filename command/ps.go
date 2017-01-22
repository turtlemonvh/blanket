package command

import (
	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
	"os"
	"strings"
	"text/tabwriter"
	"text/template"
)

var getConf client.GetTasksConf
var psConf PsConf
var quiet bool
var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List active and queued tasks",
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()
		viper.Set("logLevel", "error")
		InitializeLogging()
		ListTasks()
	},
}

type PsConf struct {
	Template string
	Quiet    bool
}

func init() {
	// Add options for tags, state, and view template
	psCmd.Flags().StringVarP(&getConf.States, "state", "s", "", "Only list tasks in these states (comma separated).")
	psCmd.Flags().StringVarP(&getConf.Types, "types", "t", "", "Only list tasks of these types (comma separated)")
	psCmd.Flags().StringVar(&getConf.RequiredTags, "requiredTags", "", "Only list tasks whose tags are a superset of these tags (comma separated)")
	psCmd.Flags().StringVar(&getConf.MaxTags, "maxTags", "", "Only list tasks whose tags are a subset of these tags (comma separated)")
	psCmd.Flags().StringVar(&psConf.Template, "template", "", "The template to use for listing tasks")
	psCmd.Flags().IntVarP(&getConf.Limit, "limit", "l", 500, "Maximum number of items to return")
	psCmd.Flags().BoolVarP(&psConf.Quiet, "quiet", "q", false, "Print ids only")
	RootCmd.AddCommand(psCmd)
}

func ListTasks() {
	tasks, err := client.GetTasks(&getConf, viper.GetInt("port"))

	if psConf.Template == "" {
		psConf.Template = "{{.id}} {{.type}} {{.state}} {{.tags}}"
	}
	if psConf.Quiet {
		psConf.Template = "{{.id}}"
	}

	// Prepare template strings for tabwriter
	psConf.Template = strings.Replace(psConf.Template, " ", "\t", -1)
	if !strings.HasSuffix(psConf.Template, "\n") {
		psConf.Template += "\n"
	}

	tmpl, err := template.New("tasks").Parse(psConf.Template)
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

	if !psConf.Quiet && len(tasks) != 0 {
		tmpl.Execute(w, headerRow)
	}
	for _, t := range tasks {
		tmpl.Execute(w, t)
	}

	w.Flush()
}
