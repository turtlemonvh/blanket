package command

import (
	"encoding/json"
	"fmt"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
	"github.com/turtlemonvh/blanket/server"
)

// Exit codes for a synchronous submit (turtlemonvh/blanket#27). Anything
// else this command exits with is the task's own exit code, so that
// `blanket submit -t deploy --wait && echo ok` does the obvious thing.
const (
	// ExitCodeError: the submission itself failed, or the task reached a
	// non-SUCCESS terminal state without an exit code of its own (killed
	// by a signal, timed out, never started).
	ExitCodeError = 1
	// ExitCodeWaitTimeout: the wait expired with the task still running.
	// 124 is what timeout(1) uses for the same situation, and it is
	// deliberately distinct from any plausible task exit code.
	ExitCodeWaitTimeout = 124
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
	Type        string
	Env         string
	Quiet       bool
	NotBefore   string
	Cron        string
	Wait        bool
	Follow      bool
	WaitTimeout string
}

func init() {
	execCmd.Flags().StringVarP(&submitConf.Type, "type", "t", "", "Run task of this type")
	execCmd.Flags().StringVarP(&submitConf.Env, "env", "e", "{}", "JSON string representing execution env for this task.")
	execCmd.Flags().BoolVarP(&submitConf.Quiet, "quiet", "q", false, "Print the task id only")
	execCmd.Flags().StringVar(&submitConf.NotBefore, "not-before", "", "Delay task start until this time: an RFC3339 timestamp, or a duration like \"10m\" relative to now. Mutually exclusive with --cron.")
	execCmd.Flags().StringVar(&submitConf.Cron, "cron", "", "Standard 5-field cron expression (minute hour dom month dow). Makes this a recurring template that spawns a child task at each fire time instead of running itself. Mutually exclusive with --not-before.")
	execCmd.Flags().BoolVar(&submitConf.Wait, "wait", false, "Block until the task finishes and print its completion payload as JSON. Exits with the task's own exit code (124 if the wait expires).")
	execCmd.Flags().BoolVarP(&submitConf.Follow, "follow", "f", false, "Like --wait, but stream the task's output to this process's stdout/stderr as it runs. Implies --wait.")
	execCmd.Flags().StringVar(&submitConf.WaitTimeout, "wait-timeout", "", "How long --wait/--follow blocks: a duration like \"60s\" or a number of seconds. Defaults to the server's tasks.sync.defaultWait; over tasks.sync.maxWait is rejected.")
	RootCmd.AddCommand(execCmd)
}

// FIXME: Include ability to send files
func SubmitTask() {
	executionEnvironment := make(map[string]interface{})
	err := json.Unmarshal([]byte(submitConf.Env), &executionEnvironment)
	if err != nil {
		log.Fatal("Error interpreting environment as valid json")
	}

	// --wait / --follow (turtlemonvh/blanket#27) turn submit into "run
	// this and tell me how it went": the process blocks, and its exit
	// code mirrors the task's. Scheduling flags are rejected rather than
	// silently ignored, because waiting on a task scheduled for later --
	// or on a recurring template, which never runs itself -- can only
	// ever time out.
	if submitConf.Wait || submitConf.Follow {
		if submitConf.NotBefore != "" || submitConf.Cron != "" {
			log.Fatal("--wait/--follow cannot be combined with --not-before/--cron: a scheduled or recurring submission has nothing to wait for")
		}
		submitAndWait(executionEnvironment)
		return
	}

	t, err := client.SubmitTaskWithOptions(submitConf.Type, executionEnvironment, viper.GetInt("port"), client.SubmitTaskOptions{
		NotBefore: submitConf.NotBefore,
		Cron:      submitConf.Cron,
	})
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

// submitAndWait implements --wait and --follow, then exits the process
// with the code described on ExitCodeError / ExitCodeWaitTimeout.
//
// Everything it prints goes straight to os.Stdout/os.Stderr rather than
// through logrus: the submit command forces logLevel=error before
// logging is initialized (see execCmd above), so a task's output routed
// through the logger would simply vanish.
func submitAndWait(env map[string]interface{}) {
	opts := client.WaitOptions{Wait: submitConf.WaitTimeout}
	port := viper.GetInt("port")

	var res client.WaitResult
	var err error

	if submitConf.Follow {
		// Log lines are written to the local stream they came from, so
		// `blanket submit --follow 2>/dev/null` keeps working the way it
		// would for the underlying command. Ordering *across* the two
		// streams is best-effort: they are tailed from two separate
		// files, so blanket cannot reconstruct the interleaving the task
		// itself produced.
		res, err = client.SubmitTaskAndStream(submitConf.Type, env, port, opts, client.StreamCallbacks{
			OnLog: func(ev server.LogEvent) error {
				w := os.Stdout
				if ev.Stream == server.LogStreamStderr {
					w = os.Stderr
				}
				fmt.Fprintln(w, ev.Line)
				return nil
			},
		})
	} else {
		res, err = client.SubmitTaskAndWait(submitConf.Type, env, port, opts)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "blanket: error running task: %s\n", err.Error())
		os.Exit(ExitCodeError)
	}

	if res.TimedOut {
		id := res.TaskId()
		if submitConf.Quiet {
			fmt.Println(id)
		}
		fmt.Fprintf(os.Stderr, "blanket: task %s did not finish in time; it is still running (poll GET /task/%s)\n", id, id)
		os.Exit(ExitCodeWaitTimeout)
	}

	payload := res.Payload
	switch {
	case submitConf.Quiet:
		fmt.Println(payload.Task.Id.Hex())
	case submitConf.Follow:
		// The output has already been printed line by line; repeating
		// the payload here would print it all twice. A one-line summary
		// on stderr keeps stdout exactly the task's own stdout.
		fmt.Fprintf(os.Stderr, "blanket: task %s finished: state=%s exitCode=%s\n",
			payload.Task.Id.Hex(), payload.Task.State, exitCodeString(payload.Task.ExitCode))
	default:
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "blanket: could not render the completion payload: %s\n", err.Error())
			os.Exit(ExitCodeError)
		}
		fmt.Println(string(encoded))
	}

	os.Exit(taskExitCode(payload))
}

// taskExitCode maps a finished task onto this process's exit status: the
// task's own code when it has one, 0/1 from its terminal state when it
// doesn't (a signalled or timed-out task has no exit code to mirror).
func taskExitCode(payload *server.CompletionPayload) int {
	if payload.Task.ExitCode != nil {
		return *payload.Task.ExitCode
	}
	if payload.Task.State == "SUCCESS" {
		return 0
	}
	return ExitCodeError
}

func exitCodeString(code *int) string {
	if code == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *code)
}
