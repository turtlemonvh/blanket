package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

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
	// ...and not just any admin token: the one in the credentials file,
	// from loopback (review L2).
	otherAdmin, _ := mint(t, s, "admin", auth.Restrictions{})
	w = call(r, "POST", "/auth/disable", otherAdmin, "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "credentials file")
	w = callFrom(r, "100.64.0.7:4444", "POST", "/auth/disable", res.Secret)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "local only")
	assert.True(t, s.AuthEnabled(), "a refused disable changes nothing")

	w = call(r, "POST", "/auth/disable", res.Secret, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"revoked":3}`, w.Body.String())
	assert.False(t, s.AuthEnabled())
	assert.NoFileExists(t, res.CredentialsPath)

	// ...and leaves a fresh setup code behind, so init works again.
	code2, _ := auth.ReadSecretFile(codePath)
	require.NotEmpty(t, code2)
	assert.Equal(t, http.StatusCreated, call(r, "POST", "/auth/init", "", `{"code":"`+code2+`"}`).Code)
}

// callFrom is call with a chosen peer address and no body.
func callFrom(r http.Handler, remote, method, path, token string) *httptest.ResponseRecorder {
	req, _ := http.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Without a credentials file there is nothing to prove local access with,
// so the running server refuses and the offline path is the way out.
func TestAuthDisableWithoutCredentialsFileIsRefused(t *testing.T) {
	s := newAuthTestServer(t)
	s.DataDir = t.TempDir()
	adminTok, _ := mint(t, s, "admin", auth.Restrictions{})
	w := call(s.GetRouter(), "POST", "/auth/disable", adminTok, "")
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "offline")
	assert.True(t, s.AuthEnabled())
}

// A flood of wrong setup codes used to queue the right one behind a second
// per guess, because the failure delay slept while holding setupMu (review
// L6). Wrong guesses still wait out the delay; the right one doesn't wait
// for them.
func TestAuthInitIsNotStalledByWrongGuesses(t *testing.T) {
	setupFailureDelay = 2 * time.Second
	t.Cleanup(func() { setupFailureDelay = defaultSetupFailureDelay })
	s := newAuthTestServer(t)
	s.DataDir = t.TempDir()
	r := s.GetRouter()
	s.startSetupIfAuthOff()
	code, err := auth.ReadSecretFile(auth.SetupCodePath(s.DataDir))
	require.NoError(t, err)

	const flood = 20
	var wg sync.WaitGroup
	wrong := make(chan int, flood)
	for i := 0; i < flood; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wrong <- call(r, "POST", "/auth/init", "", `{"code":"AAAA-AAAA"}`).Code
		}()
	}
	// Let the flood reach the handler first.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	w := call(r, "POST", "/auth/init", "", `{"code":"`+code+`"}`)
	elapsed := time.Since(start)
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Less(t, elapsed, setupFailureDelay, "the right code waited behind wrong guesses")

	wg.Wait()
	close(wrong)
	for code := range wrong {
		// Each wrong guess is still refused: 403 if it was checked before
		// the right code landed, 409 after.
		assert.Contains(t, []int{http.StatusForbidden, http.StatusConflict}, code)
	}
}

// A session opened while auth was off is bound to no user, so once auth is
// on any token could drive it, against the auth-off server and its full
// tool list (review L4). Turning auth on closes it.
func TestMCPSessionsFromAuthOffDieWhenAuthTurnsOn(t *testing.T) {
	viper.Set("mcp.enabled", true)
	viper.Set("mcp.mode", "all")
	t.Cleanup(func() { viper.Set("mcp.enabled", nil); viper.Set("mcp.mode", nil) })

	s := newAuthTestServer(t)
	ts := httptest.NewServer(s.GetRouter())
	t.Cleanup(ts.Close)

	token := ""
	hc := &http.Client{Transport: switchableBearer{token: &token}}
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: hc}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { sess.Close() })
	before, err := sess.ListTools(context.Background(), &mcp.ListToolsParams{})
	require.NoError(t, err)
	require.Contains(t, toolNamesOf(before), "blanket_launch_worker", "auth off: every tool")

	mint(t, s, "admin", auth.Restrictions{}) // auth is on now
	viewer, _ := mint(t, s, "viewer", auth.Restrictions{})
	token = viewer

	_, err = sess.ListTools(context.Background(), &mcp.ListToolsParams{})
	assert.Error(t, err, "the auth-off session must be gone, not driven by the viewer token")

	// A fresh session with the same token works, and is narrowed to it.
	assert.NotContains(t, toolNames(t, mcpSessionOverHTTP(t, s.GetRouter(), viewer)), "blanket_launch_worker")
}

// types:write honors a type restriction the way submit does (review L7).
func TestMCPWriteTaskTypeHonorsTypeRestriction(t *testing.T) {
	viper.Set("mcp.enabled", true)
	viper.Set("mcp.mode", "all")
	t.Cleanup(func() { viper.Set("mcp.enabled", nil); viper.Set("mcp.mode", nil) })
	defer setupTestTaskType(t)()

	s := newAuthTestServer(t)
	mint(t, s, "admin", auth.Restrictions{})
	writer, _ := mint(t, s, "types:write", auth.Restrictions{Types: []string{"allowed_task"}})
	sess := mcpSessionOverHTTP(t, s.GetRouter(), writer)
	toml := "command = \"echo hi\"\ntags = [\"exec:bash\"]\n"
	typesDir := viper.GetStringSlice("tasks.typesPaths")[0]
	before, _ := os.ReadFile(filepath.Join(typesDir, "echo_task.toml"))

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "blanket_write_task_type", Arguments: map[string]any{"name": "echo_task", "toml": toml, "overwrite": true},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError, "a token restricted to allowed_task may not rewrite echo_task")
	after, _ := os.ReadFile(filepath.Join(typesDir, "echo_task.toml"))
	assert.Equal(t, string(before), string(after), "echo_task.toml must be untouched")

	res, err = sess.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "blanket_write_task_type", Arguments: map[string]any{"name": "allowed_task", "toml": toml},
	})
	require.NoError(t, err)
	assert.False(t, res.IsError, "%v", res.Content)
	assert.FileExists(t, filepath.Join(typesDir, "allowed_task.toml"))
}

type switchableBearer struct{ token *string }

func (b switchableBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if *b.token != "" {
		r.Header.Set("Authorization", "Bearer "+*b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func toolNamesOf(res *mcp.ListToolsResult) []string {
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	return out
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
