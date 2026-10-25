package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// newAuthTestServer is NewTestServer with the token store wired in, as
// command/serve.go does.
func newAuthTestServer(t *testing.T) *ServerConfig {
	t.Helper()
	s, closer := NewTestServer()
	t.Cleanup(closer)
	s.Tokens = s.DB.(auth.Store)
	s.ResultsPath = t.TempDir()
	return s
}

// mint stores a token with the given scopes (names or presets) and returns
// its plaintext.
func mint(t *testing.T, s *ServerConfig, scopes string, r auth.Restrictions) (string, auth.Token) {
	t.Helper()
	ss, err := auth.ParseScopes([]string{scopes})
	require.NoError(t, err)
	res, err := s.issueToken(auth.Token{Name: scopes, Owner: "test", Scopes: ss, Restrictions: r})
	require.NoError(t, err)
	return res.Secret, res.Token
}

func call(r http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func saveTask(t *testing.T, s *ServerConfig, typ, createdBy string) tasks.Task {
	t.Helper()
	task := tasks.Task{Id: objectid.NewObjectId(), TypeId: typ, State: "WAITING", CreatedBy: createdBy}
	require.NoError(t, s.DB.SaveTask(&task))
	return task
}

// Every registered route must have a row in routeRules, and every row must
// name a registered route. The first half is the control the brief calls
// the one to stop on: a route without a row would silently need admin
// today, but a row that is *wrong* is caught only by reading the table, and
// a route with no row at all is caught here.
func TestEveryRouteHasAScope(t *testing.T) {
	viper.Set("mcp.enabled", true)
	viper.Set("mcp.mode", "all")
	t.Cleanup(func() { viper.Set("mcp.enabled", nil); viper.Set("mcp.mode", nil) })

	s := newAuthTestServer(t)
	routes := s.GetRouter().Routes()
	require.NotEmpty(t, routes)

	seen := map[string]bool{}
	for _, rt := range routes {
		_, ok := lookupRule(rt.Method, rt.Path)
		assert.True(t, ok, "route %s %s has no row in routeRules (server/auth.go)", rt.Method, rt.Path)
		m := rt.Method
		if m == http.MethodHead {
			m = http.MethodGet
		}
		seen[ruleKey(m, rt.Path)] = true
		seen[ruleKey("*", rt.Path)] = true
	}
	for k := range routeRules {
		assert.True(t, seen[k], "routeRules row %q names no registered route", k)
	}
}

func TestAuthOffAllowsEverythingAndSaysSo(t *testing.T) {
	s := newAuthTestServer(t)
	r := s.GetRouter()

	w := call(r, "GET", "/task/", "", "")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "off", w.Header().Get(AuthStateHeader))

	// Except minting a token: nobody gets the first one from the network.
	w = call(r, "POST", "/token/", "", `{"name":"x","scopes":["admin"]}`)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "blanket auth init")
}

func TestWorkerOnlyTokensDoNotTurnAuthOn(t *testing.T) {
	s := newAuthTestServer(t)
	require.NoError(t, s.Tokens.CreateToken(auth.Token{Id: objectid.NewObjectId(), Name: "w", Scopes: []auth.Scope{auth.ScopeWorker}}, "h"))
	s.refreshAuthEnabled()
	assert.False(t, s.AuthEnabled())
	mint(t, s, "viewer", auth.Restrictions{})
	assert.True(t, s.AuthEnabled())
}

func TestBadCredentialsAre401(t *testing.T) {
	s := newAuthTestServer(t)
	_, tok := mint(t, s, "admin", auth.Restrictions{})
	r := s.GetRouter()

	for name, hdr := range map[string]string{
		"none":      "",
		"malformed": "blk_nope",
		"unknown":   func() string { x, _ := auth.NewTokenString(); return x }(),
	} {
		w := call(r, "GET", "/task/", hdr, "")
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
		assert.Contains(t, w.Header().Get("WWW-Authenticate"), "Bearer", name)
		assert.Empty(t, w.Header().Get(AuthStateHeader), "auth is on")
	}

	// Expired.
	expired := tok
	expired.Id = objectid.NewObjectId()
	expired.ExpiresTs = 1
	secret, _ := auth.NewTokenString()
	require.NoError(t, s.Tokens.CreateToken(expired, auth.HashToken(secret)))
	w := call(r, "GET", "/task/", secret, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "expired")
}

// The (preset x route) table from the brief's test plan (Appendix C).
func TestPresetRouteMatrix(t *testing.T) {
	defer setupTestTaskType(t)()
	s := newAuthTestServer(t)
	adminTok, _ := mint(t, s, "admin", auth.Restrictions{})
	viewer, _ := mint(t, s, "viewer", auth.Restrictions{})
	agent, _ := mint(t, s, "agent", auth.Restrictions{})
	operator, _ := mint(t, s, "operator", auth.Restrictions{})
	r := s.GetRouter()
	task := saveTask(t, s, "echo_task", "")

	type row struct {
		method, path, body string
		want               map[string]int // token -> status
	}
	ok, created, denied, unauth := http.StatusOK, http.StatusCreated, http.StatusForbidden, http.StatusUnauthorized
	rows := []row{
		{"GET", "/version", "", map[string]int{"": ok, viewer: ok}},
		{"GET", "/task/", "", map[string]int{"": unauth, viewer: ok, agent: ok, operator: ok, adminTok: ok}},
		{"GET", "/task/" + task.Id.Hex(), "", map[string]int{"": unauth, viewer: ok, agent: ok}},
		{"POST", "/task/", `{"type":"echo_task"}`, map[string]int{"": unauth, viewer: denied, agent: created, operator: created, adminTok: created}},
		{"PUT", "/task/" + task.Id.Hex() + "/pause", "", map[string]int{viewer: denied, agent: denied}},
		{"GET", "/worker/", "", map[string]int{viewer: ok}},
		{"POST", "/worker/", `{}`, map[string]int{viewer: denied, agent: denied}},
		{"PUT", "/worker/" + objectid.NewObjectId().Hex() + "/heartbeat", "", map[string]int{viewer: denied, operator: denied}},
		{"POST", "/task/claim/" + objectid.NewObjectId().Hex(), "", map[string]int{operator: denied}},
		{"GET", "/config/", "", map[string]int{viewer: denied, operator: denied, adminTok: ok}},
		{"GET", "/ops/status/", "", map[string]int{"": unauth, viewer: ok}},
		{"POST", "/ops/backup", "", map[string]int{"": unauth, operator: denied}},
		{"GET", "/token/", "", map[string]int{"": unauth, operator: denied, adminTok: ok}},
		{"GET", "/ui/", "", map[string]int{"": unauth, viewer: ok}},
		{"POST", "/ui/workers", "", map[string]int{viewer: denied}},
		{"GET", "/no/such/route", "", map[string]int{"": unauth, operator: denied}},
	}
	for _, rw := range rows {
		for tok, want := range rw.want {
			w := call(r, rw.method, rw.path, tok, rw.body)
			assert.Equal(t, want, w.Code, "%s %s as %q: %s", rw.method, rw.path, tokenLabel(tok, adminTok, viewer, agent, operator), w.Body.String())
			if want == denied || want == unauth {
				var body map[string]string
				assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), "refusals are JSON")
				assert.NotEmpty(t, body["error"])
			}
		}
	}
}

func tokenLabel(tok, adminTok, viewer, agent, operator string) string {
	switch tok {
	case "":
		return "anonymous"
	case adminTok:
		return "admin"
	case viewer:
		return "viewer"
	case agent:
		return "agent"
	case operator:
		return "operator"
	}
	return "?"
}

func writeExtraType(t *testing.T, name string) {
	t.Helper()
	dir := viper.GetStringSlice("tasks.typesPaths")[0]
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".toml"), []byte(minimalTaskTypeToml), 0644))
}

func taskIds(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var ts []tasks.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ts))
	ids := []string{}
	for _, x := range ts {
		ids = append(ids, x.Id.Hex())
	}
	return ids
}

func TestTypeRestrictedAgent(t *testing.T) {
	defer setupTestTaskType(t)()
	writeExtraType(t, "other_task")
	s := newAuthTestServer(t)
	mint(t, s, "admin", auth.Restrictions{})
	agent, _ := mint(t, s, "agent", auth.Restrictions{Types: []string{"echo_task"}})
	r := s.GetRouter()

	w := call(r, "POST", "/task/", agent, `{"type":"other_task"}`)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())

	w = call(r, "POST", "/task/", agent, `{"type":"echo_task"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	echo := saveTask(t, s, "echo_task", "")
	other := saveTask(t, s, "other_task", "")
	ids := taskIds(t, call(r, "GET", "/task/", agent, ""))
	assert.Contains(t, ids, echo.Id.Hex())
	assert.NotContains(t, ids, other.Id.Hex(), "a type-restricted token doesn't see other types")

	w = call(r, "GET", "/task/?count=true", agent, "")
	assert.Equal(t, "2", w.Body.String(), "the count respects the restriction too")

	assert.Equal(t, http.StatusOK, call(r, "GET", "/task/"+echo.Id.Hex(), agent, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/task/"+other.Id.Hex(), agent, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/task/"+other.Id.Hex()+"/log/tail", agent, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/ui/", agent, "").Code, "UI pages can't filter, so they refuse restricted tokens")
}

func TestTaskGrantAndCreatedTasks(t *testing.T) {
	defer setupTestTaskType(t)()
	s := newAuthTestServer(t)
	mint(t, s, "admin", auth.Restrictions{})
	shared := saveTask(t, s, "echo_task", "")
	notShared := saveTask(t, s, "echo_task", "")
	tok, _ := mint(t, s, "agent", auth.Restrictions{Tasks: []string{shared.Id.Hex()}})
	r := s.GetRouter()

	assert.Equal(t, http.StatusOK, call(r, "GET", "/task/"+shared.Id.Hex(), tok, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/task/"+notShared.Id.Hex(), tok, "").Code)

	// The token can also read what it creates, though its tasks list
	// doesn't name it...
	w := call(r, "POST", "/task/", tok, `{"type":"echo_task"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var mine tasks.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &mine))
	assert.Equal(t, http.StatusOK, call(r, "GET", "/task/"+mine.Id.Hex(), tok, "").Code)
	assert.ElementsMatch(t, []string{shared.Id.Hex(), mine.Id.Hex()}, taskIds(t, call(r, "GET", "/task/", tok, "")))

	// ...and cancel it without tasks:manage, but not the shared one.
	assert.Equal(t, http.StatusOK, call(r, "PUT", "/task/"+mine.Id.Hex()+"/cancel", tok, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "PUT", "/task/"+shared.Id.Hex()+"/cancel", tok, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "DELETE", "/task/"+mine.Id.Hex(), tok, "").Code, "delete needs tasks:manage")
}

func TestSubmitOnlyTokenSeesOnlyItsOwnTasks(t *testing.T) {
	defer setupTestTaskType(t)()
	s := newAuthTestServer(t)
	mint(t, s, "admin", auth.Restrictions{})
	tok, _ := mint(t, s, "submit", auth.Restrictions{})
	r := s.GetRouter()
	other := saveTask(t, s, "echo_task", "")

	w := call(r, "POST", "/task/", tok, `{"type":"echo_task"}`)
	require.Equal(t, http.StatusCreated, w.Code)
	var mine tasks.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &mine))

	assert.Equal(t, []string{mine.Id.Hex()}, taskIds(t, call(r, "GET", "/task/", tok, "")))
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/task/"+other.Id.Hex(), tok, "").Code)
}

func TestResultsHonorRestrictions(t *testing.T) {
	s := newAuthTestServer(t)
	mint(t, s, "admin", auth.Restrictions{})
	viewer, _ := mint(t, s, "viewer", auth.Restrictions{})
	echo := saveTask(t, s, "echo_task", "")
	other := saveTask(t, s, "other_task", "")
	for _, id := range []string{echo.Id.Hex(), other.Id.Hex()} {
		require.NoError(t, os.MkdirAll(filepath.Join(s.ResultsPath, id), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(s.ResultsPath, id, "out.txt"), []byte("hi"), 0644))
	}
	restricted, _ := mint(t, s, "viewer", auth.Restrictions{Types: []string{"echo_task"}})
	r := s.GetRouter()

	assert.Equal(t, http.StatusOK, call(r, "GET", "/results/"+echo.Id.Hex()+"/out.txt", restricted, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/results/"+other.Id.Hex()+"/out.txt", restricted, "").Code)
	assert.Equal(t, http.StatusForbidden, call(r, "HEAD", "/results/"+other.Id.Hex()+"/out.txt", restricted, "").Code, "HEAD follows GET")
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/results/", restricted, "").Code, "the directory listing names every task")
	// http.FileServer cleans the path, so the check must too.
	traversal := "/results/" + echo.Id.Hex() + "/../" + other.Id.Hex() + "/out.txt"
	assert.Equal(t, http.StatusForbidden, call(r, "GET", traversal, restricted, "").Code, "dot-dot must not reach another task's directory")
	assert.Equal(t, http.StatusForbidden, call(r, "GET", "/results//"+other.Id.Hex()+"/out.txt", restricted, "").Code)
	assert.Equal(t, http.StatusOK, call(r, "GET", "/results/"+other.Id.Hex()+"/out.txt", viewer, "").Code)
	assert.Equal(t, http.StatusUnauthorized, call(r, "GET", "/results/"+echo.Id.Hex()+"/out.txt", "", "").Code)
}

func TestTokenAPI(t *testing.T) {
	s := newAuthTestServer(t)
	adminTok, adminRow := mint(t, s, "admin", auth.Restrictions{})
	r := s.GetRouter()

	w := call(r, "POST", "/token/", adminTok, `{"name":"w","scopes":["worker"]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code, "worker tokens are the server's to mint")

	w = call(r, "POST", "/token/", adminTok, `{"name":"bad","scopes":["admin"],"types":["x"]}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = call(r, "POST", "/token/", adminTok, `{"name":"ci","scopes":["agent"],"types":["echo_task"],"expires":"90d"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created TokenCreateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	assert.True(t, auth.WellFormed(created.Secret))
	assert.NotZero(t, created.ExpiresTs)
	assert.Equal(t, http.StatusOK, call(r, "GET", "/task/", created.Secret, "").Code, "the new token works")

	w = call(r, "GET", "/token/", adminTok, "")
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), created.Secret, "list never returns a plaintext")
	assert.NotContains(t, w.Body.String(), auth.HashToken(created.Secret), "...or a hash")

	assert.Equal(t, http.StatusBadRequest, call(r, "DELETE", "/token/nope", adminTok, "").Code)
	assert.Equal(t, http.StatusNotFound, call(r, "DELETE", "/token/"+objectid.NewObjectId().Hex(), adminTok, "").Code)
	assert.Equal(t, http.StatusConflict, call(r, "DELETE", "/token/"+adminRow.Id.Hex(), adminTok, "").Code, "the last admin token stays")

	assert.Equal(t, http.StatusOK, call(r, "DELETE", "/token/"+created.Id.Hex(), adminTok, "").Code)
	assert.Equal(t, http.StatusUnauthorized, call(r, "GET", "/task/", created.Secret, "").Code, "revoked")
}

func TestLastUsedIsRecorded(t *testing.T) {
	s := newAuthTestServer(t)
	tok, row := mint(t, s, "admin", auth.Restrictions{})
	r := s.GetRouter()
	require.Equal(t, http.StatusOK, call(r, "GET", "/task/", tok, "").Code)
	s.lastUsedBatcher().Wait()
	got, err := s.Tokens.TokenByHash(auth.HashToken(tok))
	require.NoError(t, err)
	assert.NotZero(t, got.LastUsedTs)
	assert.Equal(t, row.Id, got.Id)
}

func TestUIGetsAnExplanationPageWhenAuthIsOn(t *testing.T) {
	s := newAuthTestServer(t)
	mint(t, s, "admin", auth.Restrictions{})
	r := s.GetRouter()

	req, _ := http.NewRequest("GET", "/ui/", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "Authentication is on")

	// Static assets and the manifest stay open, so the page above can
	// render and an installed PWA doesn't break.
	assert.NotEqual(t, http.StatusUnauthorized, call(r, "GET", "/ui/manifest.webmanifest", "", "").Code)
	assert.Equal(t, http.StatusOK, call(r, "GET", "/version", "", "").Code)
}
