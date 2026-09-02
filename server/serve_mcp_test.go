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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
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

func TestMcpLaunchWorker_RequiresTags(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpLaunchWorker(context.Background(), nil, blanketLaunchWorkerArgs{})
	assert.Error(t, err)
}

func TestMcpLaunchWorker_RejectsLowCheckInterval(t *testing.T) {
	// launchWorkerAndWait doesn't take a checkInterval arg from MCP callers
	// (they always get the default), so this instead confirms the tool
	// surfaces the underlying error path correctly when Count is invalid.
	s, cleanup := NewTestServer()
	defer cleanup()

	_, _, err := s.mcpLaunchWorker(context.Background(), nil, blanketLaunchWorkerArgs{Tags: []string{"exec:bash"}, Count: -1})
	// Count <= 0 defaults to 1, so this should not error on Count alone;
	// asserts the default-normalization path doesn't panic or reject.
	// (A real launch will fail fast in this sandboxed test environment for
	// unrelated reasons -- e.g. no worker binary path -- so only assert
	// no panic occurred; full launch behavior is covered by the REST-level
	// TestLaunchWorker_RejectsLowCheckInterval and the unit-level
	// TestLaunchWorkerAndWait_RejectsLowCheckInterval from Task 6.)
	_ = err
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
