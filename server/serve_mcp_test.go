package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
	"github.com/turtlemonvh/blanket/worker"
)

func TestMcpModeAllows(t *testing.T) {
	cases := []struct {
		mode string
		tier mcpToolTier
		want bool
	}{
		{"readonly", mcpTierReadonly, true},
		{"readonly", mcpTierCreate, false},
		{"readonly", mcpTierAll, false},
		{"create", mcpTierReadonly, true},
		{"create", mcpTierCreate, true},
		{"create", mcpTierAll, false},
		{"all", mcpTierReadonly, true},
		{"all", mcpTierCreate, true},
		{"all", mcpTierAll, true},
		{"", mcpTierReadonly, false},
		{"bogus", mcpTierReadonly, false},
	}
	for _, c := range cases {
		got := mcpModeAllows(c.mode, c.tier)
		if got != c.want {
			t.Errorf("mcpModeAllows(%q, %v) = %v, want %v", c.mode, c.tier, got, c.want)
		}
	}
}

func TestMCPRouteMounted(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.enabled", true)
	defer viper.Set("mcp.enabled", nil)

	r := s.GetRouter()
	req, _ := http.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusNotFound, w.Code, "body: %s", w.Body.String())
}

func TestMCPRouteNotMountedWhenDisabled(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.enabled", false)
	defer viper.Set("mcp.enabled", nil)

	r := s.GetRouter()
	req, _ := http.NewRequest("POST", "/mcp", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestMcpDocs_KnownPage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	res, _, err := s.mcpDocs(context.Background(), nil, blanketDocsArgs{Page: "overview"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.NotEmpty(t, text)
}

func TestMcpDocs_UnknownPage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpDocs(context.Background(), nil, blanketDocsArgs{Page: "nope"})
	assert.Error(t, err)
}

func TestMcpTaskTypes_List(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpTaskTypes(context.Background(), nil, blanketTaskTypesArgs{})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "echo_task")
}

func TestMcpTaskTypes_Detail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpTaskTypes(context.Background(), nil, blanketTaskTypesArgs{Name: "echo_task"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "name: echo_task")
}

func TestMcpTaskTypes_UnknownName(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpTaskTypes(context.Background(), nil, blanketTaskTypesArgs{Name: "nope"})
	assert.Error(t, err)
}

func TestMcpTasks_List(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "echo_task")
	assert.Contains(t, text, "WAITING")
}

func TestMcpTasks_FilterByState(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{States: []string{"RUNNING"}})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.NotContains(t, text, "echo_task")
}

func TestMcpTasks_Detail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{Id: tsk.Id.Hex()})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, tsk.Id.Hex())
	assert.Contains(t, text, "log tail")
}

func TestMcpTasks_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpTasks(context.Background(), nil, blanketTasksArgs{Id: "not-an-id"})
	assert.Error(t, err)
}

func TestMcpWorkers_List(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Pid: 123}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	res, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, w.Id.Hex())
}

func TestMcpWorkers_Detail(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}, Pid: 123}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	res, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{Id: w.Id.Hex()})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "pid: 123")
}

func TestMcpWorkers_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpWorkers(context.Background(), nil, blanketWorkersArgs{Id: "not-an-id"})
	assert.Error(t, err)
}

func TestMcpWriteTaskType_RejectsOnError(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "broken_task",
		Toml: `tags = ["bash"]`, // missing required 'command'
	})
	assert.NoError(t, err) // a validation failure is a tool-level result, not a Go error
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "refused to write")

	typesDir := viper.GetStringSlice("tasks.typesPaths")[0]
	_, statErr := os.Stat(path.Join(typesDir, "broken_task.toml"))
	assert.True(t, os.IsNotExist(statErr), "file should not have been written")
}

func TestMcpWriteTaskType_RejectsPathTraversal(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "../../../tmp/evil",
		Toml: "command = \"echo hi\"\nexecutor = \"bash\"\ntags = [\"exec:bash\"]\ndescription = \"says hi\"\ndocumentation = \"none needed\"\n",
	})
	assert.Error(t, err, "path traversal in name should be a hard input-validation error, not a tool-level refusal")

	_, statErr := os.Stat("/tmp/evil.toml")
	assert.True(t, os.IsNotExist(statErr), "file should not have been written outside the configured types dir")
}

func TestMcpWriteTaskType_OverwriteMessage(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	toml1 := "command = \"echo hi\"\nexecutor = \"bash\"\ntags = [\"exec:bash\"]\ndescription = \"says hi\"\ndocumentation = \"none needed\"\n"
	res, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "overwrite_task",
		Toml: toml1,
	})
	assert.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "wrote")

	toml2 := "command = \"echo bye\"\nexecutor = \"bash\"\ntags = [\"exec:bash\"]\ndescription = \"says bye\"\ndocumentation = \"none needed\"\n"
	res2, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "overwrite_task",
		Toml: toml2,
	})
	assert.NoError(t, err)
	assert.Contains(t, res2.Content[0].(*mcp.TextContent).Text, "overwrote")
}

func TestMcpWriteTaskType_WritesOnSuccess(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpWriteTaskType(context.Background(), nil, blanketWriteTaskTypeArgs{
		Name: "new_task",
		Toml: "command = \"echo hi\"\nexecutor = \"bash\"\ntags = [\"exec:bash\"]\ndescription = \"says hi\"\ndocumentation = \"none needed\"\n",
	})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "wrote")

	typesDir := viper.GetStringSlice("tasks.typesPaths")[0]
	_, statErr := os.Stat(path.Join(typesDir, "new_task.toml"))
	assert.NoError(t, statErr)

	// Immediately submittable — no restart needed, since tasks.ReadTypes()
	// re-reads from disk on every request.
	tt, err := tasks.FetchTaskType("new_task")
	assert.NoError(t, err)
	assert.Equal(t, "new_task", tt.GetName())
}

func TestMcpSubmitTask_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "echo_task")
	assert.Contains(t, text, "WAITING")
}

func TestMcpSubmitTask_UnknownType(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "nope"})
	assert.Error(t, err)
}

// TestMcpSubmitTask_NotBefore confirms the MCP submit tool mirrors REST's
// notBefore handling: a future notBefore starts the task SCHEDULED
// (rather than WAITING), and the result text echoes both state and the
// schedule description.
func TestMcpSubmitTask_NotBefore(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task", NotBefore: "1h"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "state=SCHEDULED")
	assert.Contains(t, text, "schedule: Once, at")
}

// TestMcpSubmitTask_Cron confirms cron makes the submitted task a
// RECURRING template and the result echoes both the schedule description
// and the next fire time.
func TestMcpSubmitTask_Cron(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	res, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task", Cron: "*/5 * * * *"})
	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "state=RECURRING")
	assert.Contains(t, text, "schedule:")
	assert.Contains(t, text, "next fire:")
}

// TestMcpSubmitTask_NotBeforeAndCronMutuallyExclusive mirrors
// TestPostTask_NotBeforeAndCronMutuallyExclusive (scheduler_test.go) at
// the MCP layer.
func TestMcpSubmitTask_NotBeforeAndCronMutuallyExclusive(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task", NotBefore: "10m", Cron: "*/5 * * * *"})
	assert.Error(t, err)
}

// TestMcpSubmitTask_InvalidCron confirms a bad cron expression surfaces
// the parser's error rather than silently submitting a plain task.
func TestMcpSubmitTask_InvalidCron(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	_, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task", Cron: "not a cron expr"})
	assert.Error(t, err)
}

// TestMcpSubmitTask_ScheduledCapacityLimit confirms the MCP tool is
// subject to the same scheduler.maxScheduled capacity check as
// POST /task/, via the shared applyScheduleChecked helper.
func TestMcpSubmitTask_ScheduledCapacityLimit(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()
	s.SchedulerMaxScheduled = 1

	_, _, err := s.mcpSubmitTask(context.Background(), nil, blanketSubmitTaskArgs{Type: "echo_task", NotBefore: "1h"})
	assert.ErrorIs(t, err, ErrScheduledCapacityExceeded)
}

func TestMcpLaunchWorker_RequiresTags(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpLaunchWorker(context.Background(), nil, blanketLaunchWorkerArgs{})
	assert.Error(t, err)
}

func TestMcpCancelTask_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	res, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: tsk.Id.Hex()})
	assert.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "canceled")

	updated, err := s.DB.GetTask(tsk.Id)
	assert.NoError(t, err)
	assert.Equal(t, "STOPPED", updated.State)
}

func TestMcpCancelTask_WithDelete(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)

	_, _, err = s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: tsk.Id.Hex(), Delete: true})
	assert.NoError(t, err)

	_, err = s.DB.GetTask(tsk.Id)
	assert.Error(t, err)
}

func TestMcpCancelTask_DeleteOnTerminalTask(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	tsk, err := s.createTask(context.Background(), "echo_task", nil)
	assert.NoError(t, err)
	assert.NoError(t, s.DB.FinishTask(tsk.Id, database.FinishState("SUCCESS")))

	res, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: tsk.Id.Hex(), Delete: true})
	assert.NoError(t, err)
	assert.NotNil(t, res)

	_, err = s.DB.GetTask(tsk.Id)
	assert.Error(t, err, "task should have been deleted even though it was already terminal")
}

func TestMcpCancelTask_InvalidId(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpCancelTask(context.Background(), nil, blanketCancelTaskArgs{Id: "not-an-id"})
	assert.Error(t, err)
}

func TestMcpStopWorker_Valid(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	res, _, err := s.mcpStopWorker(context.Background(), nil, blanketStopWorkerArgs{Id: w.Id.Hex()})
	assert.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "stopped")

	updated, err := s.DB.GetWorker(w.Id)
	assert.NoError(t, err)
	assert.True(t, updated.Stopped)
}

func TestMcpStopWorker_WithDelete(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	w := worker.WorkerConf{Id: objectid.NewObjectId(), Tags: []string{"exec:bash"}}
	assert.NoError(t, s.DB.UpdateWorker(&w))

	_, _, err := s.mcpStopWorker(context.Background(), nil, blanketStopWorkerArgs{Id: w.Id.Hex(), Delete: true})
	assert.NoError(t, err)

	_, err = s.DB.GetWorker(w.Id)
	assert.Error(t, err)
}

func TestMCPModeGating(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer viper.Set("mcp.mode", nil)

	wantByMode := map[string][]string{
		"readonly": {"blanket_docs", "blanket_task_types", "blanket_tasks", "blanket_workers"},
		"create":   {"blanket_docs", "blanket_write_task_type", "blanket_submit_task", "blanket_launch_worker"},
		"all":      {"blanket_cancel_task", "blanket_stop_worker"},
	}

	for mode, wantNames := range wantByMode {
		viper.Set("mcp.mode", mode)
		srv := s.buildMCPServer()

		ctx := context.Background()
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		serverSession, err := srv.Connect(ctx, serverTransport, nil)
		assert.NoError(t, err)

		client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
		clientSession, err := client.Connect(ctx, clientTransport, nil)
		assert.NoError(t, err)

		res, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
		assert.NoError(t, err)

		got := map[string]bool{}
		for _, tool := range res.Tools {
			got[tool.Name] = true
		}
		for _, name := range wantNames {
			assert.True(t, got[name], "mode %q should include tool %q", mode, name)
		}
		if mode == "readonly" {
			assert.False(t, got["blanket_submit_task"], "readonly mode should not include blanket_submit_task")
			assert.False(t, got["blanket_cancel_task"], "readonly mode should not include blanket_cancel_task")
		}
		if mode == "create" {
			assert.False(t, got["blanket_cancel_task"], "create mode should not include blanket_cancel_task")
		}

		clientSession.Close()
		serverSession.Wait()
	}
}

func TestToolListFitsContextBudget(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.mode", "all") // worst case: every tool registered
	defer viper.Set("mcp.mode", nil)

	srv := s.buildMCPServer()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	assert.NoError(t, err)
	defer serverSession.Wait()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	assert.NoError(t, err)
	defer clientSession.Close()

	res, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	assert.NoError(t, err)

	toolsJSON, err := json.Marshal(res.Tools)
	assert.NoError(t, err)

	total := len(toolsJSON) + len(mcpInstructions)
	t.Logf("tools/list (%d tools) + Instructions: %d characters (budget: %d)", len(res.Tools), total, mcpContextBudgetChars)
	assert.LessOrEqual(t, total, mcpContextBudgetChars,
		"tools/list + Instructions exceeds the %d-character budget; see docs/mcp.md's levers (trim jsonschema descriptions, shorten tool descriptions, move prose into blanket_docs)", mcpContextBudgetChars)
}

// --- blanket_run_task (turtlemonvh/blanket#27) ---

// driveTaskToCompletion stands in for the worker: it waits for the task
// the tool under test just enqueued, writes the log files a real run
// would have produced, and finishes it.
func driveTaskToCompletion(t *testing.T, s *ServerConfig, state string, exitCode int, stdout, stderr string) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		tsk := awaitTask(t, s)
		writeTaskLogs(t, tsk, stdout, stderr)
		code := exitCode
		if err := s.DB.FinishTask(tsk.Id, &database.TaskFinishConfig{NewState: state, ExitCode: &code}); err != nil {
			t.Errorf("could not finish task: %v", err)
			return
		}
		s.TaskEvents.Notify()
	}()
	return done
}

// TestMcpRunTask_ReturnsCompletion: the point of the second tool is that
// one call gives the agent state, exit code and output, where
// blanket_submit_task followed by blanket_tasks would be two.
func TestMcpRunTask_ReturnsCompletion(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	done := driveTaskToCompletion(t, s, "ERROR", 3, "hello world\n", "a warning\n")
	res, _, err := s.mcpRunTask(context.Background(), nil, blanketRunTaskArgs{Type: "echo_task", WaitSeconds: 20})
	<-done

	assert.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "type: echo_task")
	assert.Contains(t, text, "state: ERROR")
	assert.Contains(t, text, "exitCode: 3")
	assert.Contains(t, text, "hello world")
	assert.Contains(t, text, "a warning")

	// And it released its wait subscription.
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount())
}

// TestMcpRunTask_WaitClampedAndTimesOut: nothing will ever claim this
// task, and waitSeconds is far over the server's cap -- so the tool must
// come back after the cap rather than after the number the model asked
// for, and say the task is still running.
func TestMcpRunTask_WaitClampedAndTimesOut(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	cleanupType := setupTestTaskType(t)
	defer cleanupType()

	viper.Set("tasks.sync.maxWait", "1s")
	defer viper.Set("tasks.sync.maxWait", nil)

	start := time.Now()
	res, _, err := s.mcpRunTask(context.Background(), nil, blanketRunTaskArgs{Type: "echo_task", WaitSeconds: 9999})
	elapsed := time.Since(start)

	assert.NoError(t, err)
	assert.Less(t, elapsed, 30*time.Second, "waitSeconds should have been clamped to tasks.sync.maxWait")
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, "did not finish within 1s")
	assert.Contains(t, text, "state: WAITING")
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount())
}

func TestMcpRunTask_UnknownType(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpRunTask(context.Background(), nil, blanketRunTaskArgs{Type: "nope"})
	assert.Error(t, err)
	assert.Equal(t, 0, s.TaskEvents.SubscriberCount(), "a rejected run must not leak a subscription")
}
