package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/turtlemonvh/blanket/lib/objectid"
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
