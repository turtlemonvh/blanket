package server

/*

Request authentication and the route -> scope table (turtlemonvh/blanket#101,
docs/design/auth.md decision row 3 and A.3).

## How a request is decided

 1. OPTIONS to a path with no OPTIONS handler passes: a CORS preflight
    carries no credentials, and the CORS handler in front of this one has
    already answered it.
 2. The route is looked up in routeRules by method and gin's route
    *pattern* (c.FullPath()), HEAD as GET. A route that is not in the
    table needs admin -- so a route added without a row fails closed, and
    TestEveryRouteHasAScope fails the build before that ever ships.
 3. Open rows (/version, static assets, the redirect) pass.
 4. While auth is off -- no token that turns it on exists (row 4) --
    everything else passes too, as the auth-off principal, and the
    response carries `X-Blanket-Auth: off`. The token-management rows are
    the exception: they need a real token even then, so nobody can mint
    the first token from the network. `blanket auth init` and its setup
    code are the only way in.
 5. Otherwise the request needs `Authorization: Bearer blk_...`. Unknown,
    malformed or expired tokens are 401; a valid token without the
    route's scope is 403.

Task routes (`/task/:id...`, `/results/<taskid>/...`) additionally load
the task and apply the token's restrictions and its createdBy grant; see
lib/auth/principal.go. Routes that cannot apply restrictions -- the UI
pages, SSE streams and worker logs, all of which show many tasks at once --
refuse restricted tokens outright rather than leak around them.

The token is never logged. ginLogger logs the path, not headers.

*/

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/database"
)

// AuthStateHeader is set to "off" on every response while auth is off, so
// a UI (phase 3's banner) or a script can tell without another request.
const AuthStateHeader = "X-Blanket-Auth"

type taskCheck int

const (
	taskCheckNone taskCheck = iota
	taskCheckRead
	taskCheckManage
	taskCheckCancel
)

type routeRule struct {
	// open routes need no credentials, even with auth on.
	open bool
	// anyOf: the principal needs at least one of these scopes.
	anyOf []auth.Scope
	// task applies the per-task checks to the route's task id.
	task taskCheck
	// unrestricted refuses tokens carrying type/task restrictions, for
	// routes that show many tasks at once and can't filter them.
	unrestricted bool
	// requireToken needs a real token even while auth is off.
	requireToken bool
}

func open() routeRule                  { return routeRule{open: true} }
func scope(ss ...auth.Scope) routeRule { return routeRule{anyOf: ss} }
func uiScope(s auth.Scope) routeRule   { return routeRule{anyOf: []auth.Scope{s}, unrestricted: true} }
func taskRule(c taskCheck) routeRule   { return routeRule{task: c} }
func tokenAdmin() routeRule {
	return routeRule{anyOf: []auth.Scope{auth.ScopeAdmin}, requireToken: true}
}
func ruleKey(method, path string) string { return method + " " + path }

var adminRule = scope(auth.ScopeAdmin)

const (
	read     = auth.ScopeRead
	submit   = auth.ScopeSubmit
	tasksMgr = auth.ScopeTasksManage
	wkrsMgr  = auth.ScopeWorkersManage
	wkr      = auth.ScopeWorker
	admin    = auth.ScopeAdmin
)

// routeRules is the one table every route is decided by. Keep it in the
// same order as GetRouter, and in sync with docs/api.md's Authentication
// section.
var routeRules = map[string]routeRule{
	ruleKey("GET", "/results/*filepath"):       taskRule(taskCheckRead),
	ruleKey("GET", "/ui/static/*filepath"):     open(),
	ruleKey("GET", "/ui/manifest.webmanifest"): open(),

	ruleKey("GET", "/ui/"):                               uiScope(read),
	ruleKey("GET", "/ui/tasks/:id"):                      uiScope(read),
	ruleKey("GET", "/ui/upcoming"):                       uiScope(read),
	ruleKey("GET", "/ui/workers"):                        uiScope(read),
	ruleKey("GET", "/ui/workers/:id"):                    uiScope(read),
	ruleKey("GET", "/ui/task-types"):                     uiScope(read),
	ruleKey("GET", "/ui/task-types/:name"):               uiScope(read),
	ruleKey("GET", "/ui/about"):                          uiScope(read),
	ruleKey("POST", "/ui/tasks"):                         uiScope(submit),
	ruleKey("POST", "/ui/workers"):                       uiScope(wkrsMgr),
	ruleKey("GET", "/ui/partials/tasks-rows"):            uiScope(read),
	ruleKey("GET", "/ui/partials/workers-rows"):          uiScope(read),
	ruleKey("GET", "/ui/partials/task-types-rows"):       uiScope(read),
	ruleKey("GET", "/ui/partials/new-task"):              uiScope(read),
	ruleKey("GET", "/ui/partials/task-type-env"):         uiScope(read),
	ruleKey("GET", "/ui/partials/task-type-warnings"):    uiScope(read),
	ruleKey("GET", "/ui/partials/schedule-preview"):      uiScope(read),
	ruleKey("GET", "/ui/partials/form-error"):            uiScope(read),
	ruleKey("GET", "/ui/partials/custom-env-row"):        uiScope(read),
	ruleKey("GET", "/ui/partials/new-worker"):            uiScope(read),
	ruleKey("GET", "/ui/partials/blank"):                 uiScope(read),
	ruleKey("GET", "/ui/partials/upcoming-onetime-rows"): uiScope(read),
	ruleKey("GET", "/ui/partials/upcoming-series-rows"):  uiScope(read),
	ruleKey("GET", "/ui/partials/series-schedule"):       uiScope(read),
	ruleKey("GET", "/ui/partials/task-log"):              uiScope(read),
	ruleKey("PUT", "/ui/series/:id/pause"):               uiScope(tasksMgr),
	ruleKey("PUT", "/ui/series/:id/resume"):              uiScope(tasksMgr),
	ruleKey("PUT", "/ui/series/:id/cancel"):              uiScope(tasksMgr),
	ruleKey("PUT", "/ui/series/:id/schedule"):            uiScope(tasksMgr),
	ruleKey("GET", "/ui/sse/tasks"):                      uiScope(read),
	ruleKey("GET", "/ui/sse/workers"):                    uiScope(read),
	ruleKey("GET", "/ui/sse/tasks/:id/log"):              uiScope(read),

	ruleKey("GET", "/"):        open(),
	ruleKey("GET", "/version"): open(),

	ruleKey("GET", "/ops/status/"):          scope(read),
	ruleKey("POST", "/ops/backup"):          scope(admin),
	ruleKey("GET", "/ops/restart/status"):   scope(admin),
	ruleKey("POST", "/ops/restart/begin"):   scope(admin),
	ruleKey("POST", "/ops/restart/pause"):   scope(admin),
	ruleKey("POST", "/ops/restart/swapped"): scope(admin),
	ruleKey("POST", "/ops/restart/drain"):   scope(admin),
	ruleKey("POST", "/ops/restart/exec"):    scope(admin),
	ruleKey("POST", "/ops/restart/abort"):   scope(admin),
	ruleKey("GET", "/config/"):              scope(admin),

	ruleKey("GET", "/task_type/"):      scope(read),
	ruleKey("GET", "/task_type/:name"): scope(read),

	// GET /task/ also answers a submit-only token, with just the tasks it
	// created; the handler filters (filterTasksFor).
	ruleKey("GET", "/task/"):                 scope(read, submit),
	ruleKey("GET", "/task/:id"):              taskRule(taskCheckRead),
	ruleKey("POST", "/task/"):                scope(submit), // the type restriction is checked in postTask
	ruleKey("DELETE", "/task/:id"):           taskRule(taskCheckManage),
	ruleKey("GET", "/task/:id/log"):          taskRule(taskCheckRead),
	ruleKey("GET", "/task/:id/log/tail"):     taskRule(taskCheckRead),
	ruleKey("PUT", "/task/:id/cancel"):       taskRule(taskCheckCancel),
	ruleKey("PUT", "/task/:id/pause"):        taskRule(taskCheckManage),
	ruleKey("PUT", "/task/:id/resume"):       taskRule(taskCheckManage),
	ruleKey("PUT", "/task/:id/schedule"):     taskRule(taskCheckManage),
	ruleKey("GET", "/schedule/describe"):     scope(read),
	ruleKey("POST", "/task/claim/:workerid"): scope(wkr),
	ruleKey("PUT", "/task/:id/run"):          scope(wkr),
	ruleKey("PUT", "/task/:id/progress"):     scope(wkr),
	ruleKey("PUT", "/task/:id/finish"):       scope(wkr),

	ruleKey("GET", "/worker/:id"):           scope(read),
	ruleKey("GET", "/worker/"):              scope(read),
	ruleKey("POST", "/worker/"):             scope(wkrsMgr),
	ruleKey("PUT", "/worker/:id/heartbeat"): scope(wkr),
	ruleKey("PUT", "/worker/:id/stop"):      scope(wkrsMgr),
	ruleKey("PUT", "/worker/:id/restart"):   scope(wkrsMgr),
	ruleKey("PUT", "/worker/:id"):           scope(wkr),
	ruleKey("DELETE", "/worker/:id"):        scope(wkrsMgr),
	ruleKey("GET", "/worker/:id/logs"):      uiScope(read),
	ruleKey("GET", "/worker/:id/log"):       uiScope(read),
	ruleKey("GET", "/worker/:id/log/tail"):  uiScope(read),

	ruleKey("GET", "/token/"):       tokenAdmin(),
	ruleKey("POST", "/token/"):      tokenAdmin(),
	ruleKey("DELETE", "/token/:id"): tokenAdmin(),

	// The setup code is init's credential, checked in the handler.
	ruleKey("GET", "/auth/status"):   open(),
	ruleKey("POST", "/auth/init"):    open(),
	ruleKey("POST", "/auth/disable"): tokenAdmin(),

	// Any token that can use at least one tool; which tools it sees and
	// may call is decided per tool (mcpToolScopes). Every method, because
	// the route is registered with r.Any.
	ruleKey("*", "/mcp"): scope(read, submit, tasksMgr, auth.ScopeTypesWrite, wkrsMgr),
}

// lookupRule finds a route's row. HEAD follows GET; a "*" row matches
// every method.
func lookupRule(method, fullPath string) (routeRule, bool) {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	if r, ok := routeRules[ruleKey(method, fullPath)]; ok {
		return r, true
	}
	r, ok := routeRules[ruleKey("*", fullPath)]
	return r, ok
}

type principalCtxKey struct{}

const principalGinKey = "blanket.principal"

// principalFrom returns the request's principal. A handler reached without
// the middleware (a test calling it directly) gets the auth-off principal,
// which is what such a router has always meant.
func principalFrom(c *gin.Context) *auth.Principal {
	if v, ok := c.Get(principalGinKey); ok {
		if p, ok := v.(*auth.Principal); ok {
			return p
		}
	}
	return auth.AuthOffPrincipal()
}

// principalFromContext is principalFrom for code holding only a
// context.Context derived from the request (the MCP handler).
func principalFromContext(ctx context.Context) (*auth.Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(*auth.Principal)
	return p, ok
}

func setPrincipal(c *gin.Context, p *auth.Principal) {
	c.Set(principalGinKey, p)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), principalCtxKey{}, p))
}

// AuthEnabled reports whether auth is on: a token that turns it on exists
// (row 4). Cached, and recomputed after every token change this server
// makes -- the server holds the database lock, so nothing else can change
// the tokens underneath it. A store error fails closed.
func (s *ServerConfig) AuthEnabled() bool {
	if s.Tokens == nil {
		return false
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.authKnown {
		return s.authOn
	}
	ts, err := s.Tokens.ListTokens()
	if err != nil {
		log.WithField("err", err).Error("could not read the tokens bucket; treating auth as on")
		return true
	}
	s.authOn, s.authKnown = auth.AnyEnablesAuth(ts), true
	return s.authOn
}

// refreshAuthEnabled drops the cached answer after a token change.
func (s *ServerConfig) refreshAuthEnabled() {
	s.authMu.Lock()
	s.authKnown = false
	s.authMu.Unlock()
}

func (s *ServerConfig) lastUsedBatcher() *auth.LastUsedBatcher {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.lastUsed == nil {
		s.lastUsed = auth.NewLastUsedBatcher(s.Tokens)
	}
	return s.lastUsed
}

// authenticate resolves a bearer token. On failure it returns the status
// and message to answer with.
func (s *ServerConfig) authenticate(r *http.Request) (*auth.Principal, int, string) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return nil, http.StatusUnauthorized, "authentication required: send Authorization: Bearer <token>"
	}
	tok := strings.TrimSpace(h[7:])
	if !auth.WellFormed(tok) || s.Tokens == nil {
		return nil, http.StatusUnauthorized, "invalid token"
	}
	// Looked up by its SHA-256, so no secret is ever compared byte by
	// byte: timing on the lookup can only leak bits of a hash the caller
	// can't steer.
	hash := auth.HashToken(tok)
	t, err := s.Tokens.TokenByHash(hash)
	if err == auth.ErrTokenNotFound {
		return nil, http.StatusUnauthorized, "invalid token"
	}
	if err != nil {
		log.WithField("err", err).Error("token lookup failed")
		return nil, http.StatusInternalServerError, "token lookup failed"
	}
	if t.Expired(time.Now()) {
		return nil, http.StatusUnauthorized, "token expired"
	}
	s.lastUsedBatcher().Touch(hash)
	return auth.NewPrincipal(t), 0, ""
}

func (s *ServerConfig) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		rule, known := lookupRule(c.Request.Method, c.FullPath())
		if !known && c.Request.Method == http.MethodOptions {
			// A CORS preflight: the CORS handler in front of this one
			// has already answered it, and no route handler will run.
			// (OPTIONS on a route that does have a handler, like /mcp,
			// is decided by its row like any other method.)
			c.Next()
			return
		}
		if !known {
			rule = adminRule
		}
		enabled := s.AuthEnabled()
		if !enabled {
			c.Header(AuthStateHeader, "off")
		}
		if rule.open || (!enabled && !rule.requireToken) {
			setPrincipal(c, auth.AuthOffPrincipal())
			c.Next()
			return
		}
		if !enabled {
			abortAuth(c, http.StatusForbidden, "authentication is not set up on this server; run `blanket auth init` first")
			return
		}
		p, status, msg := s.authenticate(c.Request)
		if p == nil {
			abortAuth(c, status, msg)
			return
		}
		if status, msg := s.authorize(c, p, rule); status != 0 {
			abortAuth(c, status, msg)
			return
		}
		setPrincipal(c, p)
		c.Next()
	}
}

func hasAny(p *auth.Principal, ss []auth.Scope) bool {
	for _, s := range ss {
		if p.Has(s) {
			return true
		}
	}
	return false
}

func forbidden(scopes ...auth.Scope) (int, string) {
	names := make([]string, len(scopes))
	for i, s := range scopes {
		names[i] = string(s)
	}
	return http.StatusForbidden, "this token lacks the scope this route needs (" + strings.Join(names, " or ") + ")"
}

// authorize applies rule to an authenticated principal. 0 means allowed.
func (s *ServerConfig) authorize(c *gin.Context, p *auth.Principal, rule routeRule) (int, string) {
	if rule.unrestricted && p.Restricted() {
		return http.StatusForbidden, "this route shows many tasks at once, so it refuses tokens with type or task restrictions"
	}
	if rule.task == taskCheckNone {
		if !hasAny(p, rule.anyOf) {
			return forbidden(rule.anyOf...)
		}
		return 0, ""
	}

	base := auth.ScopeRead
	if rule.task != taskCheckRead {
		base = auth.ScopeTasksManage
	}
	id := c.Param("id")
	if strings.HasPrefix(c.FullPath(), "/results/") {
		id = resultsTaskId(c.Param("filepath"))
	}
	// An unrestricted token holding the route's scope may act on any
	// task, so there is nothing to look up: the handler reports a bad or
	// missing id the usual way (400/404).
	if p.Has(base) && !p.Restricted() {
		return 0, ""
	}
	taskId, err := SafeObjectId(id)
	if err != nil {
		return forbidden(base)
	}
	t, err := s.DB.GetTask(taskId)
	if _, missing := err.(database.ItemNotFoundError); missing {
		if p.Has(base) {
			return 0, "" // the handler answers 404
		}
		return forbidden(base)
	}
	if err != nil {
		return http.StatusInternalServerError, "could not load the task to check access"
	}
	ref := taskRef(t)
	var ok bool
	switch rule.task {
	case taskCheckRead:
		ok = p.CanReadTask(ref)
	case taskCheckManage:
		ok = p.CanManageTask(ref)
	case taskCheckCancel:
		ok = p.CanCancelTask(ref)
	}
	if !ok {
		return http.StatusForbidden, "this token may not access this task"
	}
	return 0, ""
}

// resultsTaskId is the task id a /results/*filepath request will actually
// be served from: the first segment of the path *after* cleaning, since
// that is what http.FileServer serves. Checking the raw first segment
// would let /results/<allowed>/../<other>/file through to another task's
// directory.
func resultsTaskId(filepath string) string {
	cleaned := path.Clean("/" + filepath)
	return strings.SplitN(strings.TrimPrefix(cleaned, "/"), "/", 2)[0]
}

// abortAuth answers a refused request. JSON like every other error, except
// a browser asking for a UI page, which gets a page explaining why: the
// UI has no login until auth phase 3.
func abortAuth(c *gin.Context, status int, msg string) {
	if status == http.StatusUnauthorized {
		c.Header("WWW-Authenticate", `Bearer realm="blanket"`)
	}
	if strings.HasPrefix(c.Request.URL.Path, "/ui/") && strings.Contains(c.GetHeader("Accept"), "text/html") {
		c.Data(status, "text/html; charset=utf-8", []byte(uiAuthPage))
		c.Abort()
		return
	}
	c.AbortWithStatusJSON(status, gin.H{"error": msg})
}

const uiAuthPage = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Blanket: sign-in required</title></head>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 3rem auto; padding: 0 1rem; line-height: 1.5">
<h1>Authentication is on</h1>
<p>This blanket server requires an API token, and the web UI cannot sign in yet:
browser login arrives in a later release (turtlemonvh/blanket#101, phase 3).</p>
<p>Until then, use the <code>blanket</code> CLI on the server's host (it reads the local
credentials file), or the REST API and MCP endpoint with an
<code>Authorization: Bearer blk_…</code> header. See <code>blanket token --help</code>.</p>
</body></html>
`
