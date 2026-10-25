package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/auth"
)

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm(), "%s must be readable by its owner only", path)
	}
}

func TestAuthInitAndDisable(t *testing.T) {
	setupFailureDelay = 0
	t.Cleanup(func() { setupFailureDelay = defaultSetupFailureDelay })
	defer setupTestTaskType(t)()
	s := newAuthTestServer(t)
	s.DataDir = t.TempDir()
	r := s.GetRouter()

	s.startSetupIfAuthOff()
	codePath := auth.SetupCodePath(s.DataDir)
	assertOwnerOnly(t, codePath)
	code, err := auth.ReadSecretFile(codePath)
	require.NoError(t, err)
	require.Len(t, code, 9)

	w := call(r, "GET", "/auth/status", "", "")
	assert.JSONEq(t, `{"enabled":false,"setupPending":true}`, w.Body.String())

	// Without the code: 403, even though auth is off.
	assert.Equal(t, http.StatusForbidden, call(r, "POST", "/auth/init", "", `{"code":"AAAA-AAAA"}`).Code)
	assert.Equal(t, http.StatusForbidden, call(r, "POST", "/auth/init", "", `{}`).Code)
	assert.False(t, s.AuthEnabled())

	// Case and dashes don't matter.
	w = call(r, "POST", "/auth/init", "", `{"code":"`+code[:4]+code[5:]+`"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var res AuthInitResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.True(t, s.AuthEnabled())
	assert.Equal(t, []auth.Scope{auth.ScopeAdmin}, res.Scopes)
	assert.Zero(t, res.ExpiresTs, "init's admin token never expires")

	assertOwnerOnly(t, res.CredentialsPath)
	saved, _ := auth.ReadSecretFile(res.CredentialsPath)
	assert.Equal(t, res.Secret, saved)
	assert.NoFileExists(t, codePath, "the setup code is single-use")

	// Single use, and auth is on now anyway.
	assert.Equal(t, http.StatusConflict, call(r, "POST", "/auth/init", "", `{"code":"`+code+`"}`).Code)

	// The smoke-test assertions: anonymous submit is refused, the token works.
	assert.Equal(t, http.StatusUnauthorized, call(r, "POST", "/task/", "", `{"type":"echo_task"}`).Code)
	assert.Equal(t, http.StatusCreated, call(r, "POST", "/task/", res.Secret, `{"type":"echo_task"}`).Code)

	// Disable needs the admin token...
	assert.Equal(t, http.StatusUnauthorized, call(r, "POST", "/auth/disable", "", "").Code)
	viewer, _ := mint(t, s, "viewer", auth.Restrictions{})
	assert.Equal(t, http.StatusForbidden, call(r, "POST", "/auth/disable", viewer, "").Code)

	w = call(r, "POST", "/auth/disable", res.Secret, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"revoked":2}`, w.Body.String())
	assert.False(t, s.AuthEnabled())
	assert.NoFileExists(t, res.CredentialsPath)

	// ...and leaves a fresh setup code behind, so init works again.
	code2, _ := auth.ReadSecretFile(codePath)
	require.NotEmpty(t, code2)
	assert.Equal(t, http.StatusCreated, call(r, "POST", "/auth/init", "", `{"code":"`+code2+`"}`).Code)
}

// mcpToolsOverHTTP lists tools through the real /mcp route with token, so
// it covers the middleware, the per-scope server cache and TokenInfo.
func mcpSessionOverHTTP(t *testing.T, r http.Handler, token string) *mcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	hc := &http.Client{Transport: bearerRoundTripper{token: token, base: http.DefaultTransport}}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: hc}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { sess.Close() })
	return sess
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

func toolNames(t *testing.T, sess *mcp.ClientSession) []string {
	t.Helper()
	res, err := sess.ListTools(context.Background(), &mcp.ListToolsParams{})
	require.NoError(t, err)
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	sort.Strings(out)
	return out
}

func TestMCPToolsListFollowsScopes(t *testing.T) {
	viper.Set("mcp.enabled", true)
	viper.Set("mcp.mode", "all")
	t.Cleanup(func() { viper.Set("mcp.enabled", nil); viper.Set("mcp.mode", nil) })
	defer setupTestTaskType(t)()

	s := newAuthTestServer(t)
	adminTok, _ := mint(t, s, "admin", auth.Restrictions{})
	viewer, _ := mint(t, s, "viewer", auth.Restrictions{})
	agent, _ := mint(t, s, "agent", auth.Restrictions{Types: []string{"other_task"}})
	operator, _ := mint(t, s, "operator", auth.Restrictions{})
	r := s.GetRouter()

	read := []string{"blanket_docs", "blanket_task_logs", "blanket_task_types", "blanket_tasks", "blanket_workers"}
	all := []string{"blanket_cancel_task", "blanket_docs", "blanket_launch_worker", "blanket_run_task", "blanket_stop_worker",
		"blanket_submit_task", "blanket_task_logs", "blanket_task_types", "blanket_tasks", "blanket_workers", "blanket_write_task_type"}

	assert.Equal(t, read, toolNames(t, mcpSessionOverHTTP(t, r, viewer)))
	assert.Equal(t, []string{"blanket_cancel_task", "blanket_docs", "blanket_run_task", "blanket_submit_task",
		"blanket_task_logs", "blanket_task_types", "blanket_tasks", "blanket_workers"},
		toolNames(t, mcpSessionOverHTTP(t, r, agent)))
	assert.Equal(t, all, toolNames(t, mcpSessionOverHTTP(t, r, operator)))
	assert.Equal(t, all, toolNames(t, mcpSessionOverHTTP(t, r, adminTok)))

	// mcp.mode stays the ceiling: scopes only narrow it.
	viper.Set("mcp.mode", "readonly")
	s2 := newAuthTestServer(t)
	op2, _ := mint(t, s2, "operator", auth.Restrictions{})
	assert.Equal(t, read, toolNames(t, mcpSessionOverHTTP(t, s2.GetRouter(), op2)))
	viper.Set("mcp.mode", "all")

	// A viewer can't call a tool it wasn't given.
	_, err := mcpSessionOverHTTP(t, r, viewer).CallTool(context.Background(), &mcp.CallToolParams{
		Name: "blanket_submit_task", Arguments: map[string]any{"type": "echo_task"},
	})
	assert.Error(t, err)

	// The agent's type restriction applies inside the tool too.
	res, err := mcpSessionOverHTTP(t, r, agent).CallTool(context.Background(), &mcp.CallToolParams{
		Name: "blanket_submit_task", Arguments: map[string]any{"type": "echo_task"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError, "a type-restricted token may not submit other types over MCP either")

	// No token: the route refuses before MCP sees the request.
	w := call(r, "POST", "/mcp", "", `{}`)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// With auth on, a tool call that somehow reaches a handler without the
// caller's TokenInfo is refused rather than run as nobody-in-particular.
func TestMCPCallerFailsClosedWithAuthOn(t *testing.T) {
	s := newAuthTestServer(t)
	_, err := s.mcpCaller(&mcp.CallToolRequest{})
	assert.NoError(t, err, "auth off: in-process sessions keep working")
	mint(t, s, "admin", auth.Restrictions{})
	_, err = s.mcpCaller(&mcp.CallToolRequest{})
	assert.Error(t, err)
}

func TestEveryMCPToolHasScopes(t *testing.T) {
	viper.Set("mcp.mode", "all")
	t.Cleanup(func() { viper.Set("mcp.mode", nil) })
	s, closer := NewTestServer()
	defer closer()
	// addMCPTool panics on a tool with no mcpToolScopes row; building the
	// full server registers every tool.
	assert.NotPanics(t, func() { s.buildMCPServer() })
}
