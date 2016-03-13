package tasks

import (
	"github.com/stretchr/testify/assert"
	"strings"
	"testing"
)

func TestGenerateFromTaskType(t *testing.T) {
	tt_config := `
tags = ["bash", "unix"]

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
	tt, err := readTaskType(strings.NewReader(tt_config))
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
	assert.Equal(t, cmd.Path, "/bin/bash")
	assert.True(t, len(cmd.Args) > 1)

}
