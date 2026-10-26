package command

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/client"
	"github.com/turtlemonvh/blanket/lib/objectid"
)

var rmConf RmConf
var rmCmd = &cobra.Command{
	Use:   "rm",
	Short: "Remove tasks",
	Long: `Remove a task's record and result directory.

A task that is still running (CLAIMED or RUNNING) is refused; cancel it
first, or pass --force to cancel it (with force) and delete it once its
worker confirms the process is gone.`,
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

// rmForceWait bounds how long `rm --force` keeps retrying the delete of a
// task it has just cancelled; a worker polls every 2s by default.
var (
	rmForceWait = 15 * time.Second
	rmForcePoll = 250 * time.Millisecond
)

// FIXME: Accept stdin
func init() {
	// Add options for tags, state, and view template
	rmCmd.Flags().BoolVarP(&rmConf.Force, "force", "f", false, "Cancel a running task first, and don't fail if the task is already gone")
	RootCmd.AddCommand(rmCmd)
}

func (c *RmConf) RemoveTask(taskId string) {
	port := viper.GetInt("port")
	del := func() error { return client.DeleteTask(taskId, port) }
	var err error
	if c.Force && objectid.IsObjectIdHex(taskId) {
		cancel := func() error { return client.CancelTask(taskId, true, port) }
		err = forceRemoveTask(del, cancel, rmForceWait, rmForcePoll)
	} else {
		err = del()
	}
	if err == nil {
		return
	}
	if msg, ok := rmNotFoundMessage(taskId, err); ok {
		if c.Force {
			// --force: a task that is already gone is not an error.
			return
		}
		fmt.Fprintln(os.Stderr, msg)
	} else {
		printAPIError(err)
	}
	os.Exit(1)
}

// forceRemoveTask is `rm --force` (turtlemonvh/blanket#252). The server
// refuses to delete a task that may still be running with a 400, so on a
// 400 it force-cancels the task and retries the delete until it lands or
// wait runs out. The retries cover both the force cancel's short wait for
// the worker to confirm the kill and a task caught in CLAIMED, which
// can't be cancelled until it reaches RUNNING a moment later. The caller
// has already checked the id is well formed, so a 400 here is never a
// malformed id. Any other error is returned at once.
func forceRemoveTask(del, cancel func() error, wait, poll time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := del()
		if !isAPIStatus(err, http.StatusBadRequest) {
			return err
		}
		// Errors are expected here (a task already STOPPED, or one
		// still CLAIMED, is not cancelable) and the delete's own answer
		// is what decides the outcome, so they are ignored.
		_ = cancel()
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(poll)
	}
}

func isAPIStatus(err error, status int) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// rmNotFoundMessage turns the server's 404 for a missing task
// (turtlemonvh/blanket#249) into a plain "no such task" line, keeping
// printAPIError's "error: <status> ..." shape. ok is false for any other
// error, which the caller prints as usual.
func rmNotFoundMessage(taskId string, err error) (string, bool) {
	if !isAPIStatus(err, http.StatusNotFound) {
		return "", false
	}
	var apiErr *client.APIError
	errors.As(err, &apiErr)
	return fmt.Sprintf("error: %d no such task %s", apiErr.Status, taskId), true
}
