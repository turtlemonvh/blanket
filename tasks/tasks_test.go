package tasks

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestGenerateFromTaskType(t *testing.T) {
	tt_config := `
tags = ["exec:bash", "os:unix"]

# timeout in seconds
timeout = 200

# The command to execute
command='''
{{.DEFAULT_COMMAND}}
'''

executor="bash"

    [[environment.default]]
    name = "ANIMAL"
    value = "giraffe"

    [[environment.required]]
    name = "DEFAULT_COMMAND"
    description = "The bash command to run. E.g. 'echo $(date)'"
`
	tt, err := ReadTaskType(strings.NewReader(tt_config))
	tt.Config.Set("name", "bash_task")
	assert.Equal(t, err, nil)
	assert.Equal(t, tt.ConfigFile, "")
	assert.NotEqual(t, tt.LoadedTs, 0)
	assert.Equal(t, tt.ConfigVersionHash, "")

	newEnv := map[string]string{
		"DEFAULT_COMMAND": "echo 'hello'",
	}
	var nt Task
	nt, err = tt.NewTask(newEnv)
	assert.Equal(t, err, nil)
	assert.Equal(t, nt.Pid, 0)
	assert.Equal(t, nt.TypeId, "bash_task")

	cmd, err := nt.GetCmd(&tt)
	assert.NoError(t, err)
	assert.Contains(t, cmd.Path, "bash")
	assert.True(t, len(cmd.Args) > 1)

}

// Pins the argv each executor is invoked with. These flags are easy to
// drop in a refactor and nothing else would notice: the powershell ones
// only surface as a task quietly inheriting the machine owner's profile,
// or as a prompt hanging a task until its timeout
// (turtlemonvh/blanket#169).
func TestGetCmd_ExecutorArgs(t *testing.T) {
	cases := []struct {
		executor string
		wantArgs []string
	}{
		// bash -c is already non-interactive, so it never sources
		// ~/.bashrc; the powershell flags buy it the same property.
		{"bash", []string{"-c", "echo hi"}},
		{"cmd", []string{"/c", "echo hi"}},
		{"powershell", []string{"-NoProfile", "-NonInteractive", "-Command", "echo hi"}},
		// Anything else is assumed to take -c, unchanged.
		{"zsh", []string{"-c", "echo hi"}},
	}

	for _, tc := range cases {
		t.Run(tc.executor, func(t *testing.T) {
			config := fmt.Sprintf("description = %q\ncommand = %q\nexecutor = %q\n",
				"executor arg test", "echo hi", tc.executor)

			tt, err := ReadTaskType(strings.NewReader(config))
			assert.NoError(t, err)
			tt.Config.Set("name", "executor_arg_test")

			nt, err := tt.NewTask(map[string]string{})
			assert.NoError(t, err)

			cmd, err := nt.GetCmd(&tt)
			assert.NoError(t, err)
			// Args[0] is the resolved program name, which varies with
			// platform and PATH; the flags after it are what matter.
			assert.Equal(t, tc.wantArgs, cmd.Args[1:])
		})
	}
}
