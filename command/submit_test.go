package command

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/server"
	"github.com/turtlemonvh/blanket/tasks"
)

// The contract that makes `blanket submit --wait` usable in a pipeline:
// the process exits with the task's own exit code, so
// `blanket submit -t deploy --wait && echo ok` does the obvious thing.
// A task with no exit code of its own (killed by a signal, timed out,
// never started) falls back to its terminal state --
// turtlemonvh/blanket#27.
func TestTaskExitCode(t *testing.T) {
	code := func(c int) *int { return &c }

	cases := []struct {
		name     string
		state    string
		exitCode *int
		want     int
	}{
		{"clean exit", "SUCCESS", code(0), 0},
		{"exit 3 is exit 3, not just 'failed'", "ERROR", code(3), 3},
		{"exit 1", "ERROR", code(1), 1},
		{"signalled task has no code of its own", "STOPPED", nil, ExitCodeError},
		{"timed out task has no code of its own", "TIMEDOUT", nil, ExitCodeError},
		{"success without a recorded code", "SUCCESS", nil, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := &server.CompletionPayload{
				Task: tasks.Task{State: tc.state, ExitCode: tc.exitCode},
			}
			assert.Equal(t, tc.want, taskExitCode(payload))
		})
	}
}

func TestExitCodeString(t *testing.T) {
	c := 3
	assert.Equal(t, "3", exitCodeString(&c))
	assert.Equal(t, "unknown", exitCodeString(nil))
}
