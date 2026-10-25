package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/tasks"
)

// blanket_task_logs (turtlemonvh/blanket#231). SearchLog's own edge cases
// are in log_search_test.go; these cover the tool around it.

// taskWithLogs creates a task and writes its two log files (closed again
// before returning, so nothing is held open on Windows).
func taskWithLogs(t *testing.T, s *ServerConfig, stdout, stderr string) tasks.Task {
	t.Helper()
	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)
	writeTaskLogs(t, tsk, stdout, stderr)
	return tsk
}

func taskLogsText(t *testing.T, s *ServerConfig, args blanketTaskLogsArgs) string {
	t.Helper()
	res, _, err := s.mcpTaskLogs(context.Background(), nil, args)
	if !assert.NoError(t, err) {
		return ""
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestMcpTaskLogs_GrepWithContext(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()

	tsk := taskWithLogs(t, s, "a\nb\nboom: disk full\nc\nd\ne\nf\ng\nboom again\nh\n", "")
	one := 1
	text := taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Grep: `^boom`, Context: &one})
	assert.Equal(t, "stdout: matches 1-2 of 2 for /^boom/ (10 lines)\n"+
		"2-b\n3:boom: disk full\n4-c\n--\n8-g\n9:boom again\n10-h\n", text)

	// Unset context means the default of 2.
	text = taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Grep: `disk`})
	assert.Contains(t, text, "1-a\n2-b\n3:boom: disk full\n4-c\n5-d\n")
}

func TestMcpTaskLogs_GrepPagingAndTail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()

	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	tsk := taskWithLogs(t, s, b.String(), "")
	zero := 0

	text := taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Grep: `0$`, Context: &zero, Limit: 3})
	assert.Contains(t, text, "matches 1-3 of 10")
	assert.Contains(t, text, "10:line 10\n--\n20:line 20\n--\n30:line 30\n")
	assert.Contains(t, text, "more: offset=3")

	text = taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Grep: `0$`, Context: &zero, Offset: 3, Limit: 3})
	assert.Contains(t, text, "40:line 40\n--\n50:line 50\n--\n60:line 60\n")

	text = taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Offset: -2})
	assert.Equal(t, "stdout: lines 99-100 of 100\n99:line 99\n100:line 100\n", text)
}

func TestMcpTaskLogs_Stderr(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()

	tsk := taskWithLogs(t, s, "out\n", "err line\n")
	text := taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Stream: "stderr"})
	assert.Contains(t, text, "1:err line")
	assert.NotContains(t, text, "out")
}

func TestMcpTaskLogs_InvalidRegex(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()

	tsk := taskWithLogs(t, s, "x\n", "")
	_, _, err := s.mcpTaskLogs(context.Background(), nil, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Grep: `(unclosed`})
	assert.ErrorContains(t, err, "invalid grep regex")
}

func TestMcpTaskLogs_MissingLogAndBadStream(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	_, _, err = s.mcpTaskLogs(context.Background(), nil, blanketTaskLogsArgs{Id: tsk.Id.Hex()})
	assert.ErrorContains(t, err, "has no stdout log yet")

	_, _, err = s.mcpTaskLogs(context.Background(), nil, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Stream: "both"})
	assert.ErrorContains(t, err, "stream must be stdout or stderr")

	_, _, err = s.mcpTaskLogs(context.Background(), nil, blanketTaskLogsArgs{Id: "not-an-id"})
	assert.ErrorContains(t, err, "not a valid task id")
}

func TestMcpTaskLogs_LimitClampedToMaxLogLines(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()
	viper.Set("mcp.maxLogLines", 5)
	defer viper.Set("mcp.maxLogLines", nil)

	tsk := taskWithLogs(t, s, strings.Repeat("x\n", 20), "")
	text := taskLogsText(t, s, blanketTaskLogsArgs{Id: tsk.Id.Hex(), Limit: 1000})
	assert.Contains(t, text, "lines 1-5 of 20")
	assert.Contains(t, text, "more: offset=5")
}

func TestMcpTaskLogs_OversizedPageRefused(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer setupTestTaskType(t)()

	// 50 lines at the 2,000-char line cap is ~100k characters, well over
	// the page cap: refused with advice, not cut short.
	tsk := taskWithLogs(t, s, strings.Repeat(strings.Repeat("q", 3000)+"\n", 50), "")
	_, _, err := s.mcpTaskLogs(context.Background(), nil, blanketTaskLogsArgs{Id: tsk.Id.Hex()})
	assert.ErrorIs(t, err, ErrLogPageTooLarge)
	assert.ErrorContains(t, err, "smaller limit")
}
