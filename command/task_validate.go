package command

import (
	"fmt"
	"os"
	"os/exec"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/turtlemonvh/blanket/tasks"
)

var taskValidateCmd = &cobra.Command{
	Use:   "task-validate [type-name]",
	Short: "Validate that task types are runnable",
	Long:  `Checks that each task type's executor is on $PATH and that the command field is non-empty.`,
	Run: func(cmd *cobra.Command, args []string) {
		InitializeConfig()

		tts, err := tasks.ReadTypes()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading task types: %s\n", err)
			os.Exit(1)
		}

		if len(args) > 0 {
			name := args[0]
			found := false
			for i := range tts {
				if tts[i].GetName() == name {
					tts = []tasks.TaskType{tts[i]}
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "Task type %q not found\n", name)
				os.Exit(1)
			}
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tEXECUTOR\tCOMMAND\tSTATUS")

		anyFailed := false
		for _, tt := range tts {
			executor := tt.Config.GetString("executor")
			if executor == "" {
				executor = "bash"
			}
			command := tt.Config.GetString("command")

			status := "ok"
			if command == "" {
				status = "missing command"
				anyFailed = true
			} else if _, err := exec.LookPath(executor); err != nil {
				status = fmt.Sprintf("executor not found: %s", executor)
				anyFailed = true
			}

			cmdDisplay := command
			if len(cmdDisplay) > 40 {
				cmdDisplay = cmdDisplay[:37] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", tt.GetName(), executor, cmdDisplay, status)
		}
		w.Flush()

		if anyFailed {
			os.Exit(1)
		}
	},
}
