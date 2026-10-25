package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

	"github.com/turtlemonvh/blanket/lib/auth"
	"github.com/turtlemonvh/blanket/lib/docs"
)

// mcpToolTier says which mcp.mode values register a given tool.
// "readonly" tools are visible in every mode; "create" tools need mode
// create or all; "all" (destructive) tools need mode all.
type mcpToolTier int

const (
	mcpTierReadonly mcpToolTier = iota
	mcpTierCreate
	mcpTierAll
)

// mcpModeAllows reports whether a tool of tier t should be registered
// when the server is configured for mode.
func mcpModeAllows(mode string, t mcpToolTier) bool {
	switch mode {
	case "readonly":
		return t == mcpTierReadonly
	case "create":
		return t == mcpTierReadonly || t == mcpTierCreate
	case "all":
		return true
	default:
		return false
	}
}

// Tool annotations, one set per kind of tool (turtlemonvh/blanket#229).
// They let a client auto-approve the reads and warn before a destructive
// call. The spec has clients treat annotations from a server as
// untrusted hints, so they sit alongside mcp.mode rather than replacing
// it: mode decides what is registered at all.
//
// go-sdk always serializes readOnlyHint and idempotentHint, so leaving
// them at false is still an explicit false on the wire. destructiveHint
// is a pointer whose spec default is true, so the additive tools must
// set false; the destructive ones set true explicitly rather than lean
// on a default a client might not apply.
var (
	mcpReadOnlyAnnotations    = &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcpAdditiveAnnotations    = &mcp.ToolAnnotations{DestructiveHint: new(false)}
	mcpDestructiveAnnotations = &mcp.ToolAnnotations{DestructiveHint: new(true)}
)

// mcpContextBudgetChars is a design tripwire, not a hard protocol limit —
// the measured tools/list + Instructions size (currently ~6590 chars) is
// close to this budget by design; eleven tools' worth of descriptions is
// a lean surface, not accumulated bloat. It has been raised four times,
// each time for a tool surface that grew rather than for prose that
// sprawled:
// 4000 -> 4400 when blanket_submit_task and blanket_cancel_task grew
// notBefore/cron and wider-state descriptions (turtlemonvh/blanket#61's
// pause/resume/schedule rework), and 4400 -> 5000 for the tenth tool,
// blanket_run_task (turtlemonvh/blanket#27) — submit-and-wait is the
// interaction an agent actually wants, and a tool it can't see is worth
// nothing. Descriptions were trimmed first; the raise covers what was
// left. Then 5000 -> 6000 for tool annotations and
// blanket_write_task_type's overwrite argument (turtlemonvh/blanket#229):
// 4986 -> 5822, about 750 of it the annotations objects, which go-sdk
// serializes in full on every tool and which carry no prose to trim.
// They are what lets a client auto-approve reads and warn before a
// destructive call. Then 6000 -> 7000 for the eleventh tool,
// blanket_task_logs (turtlemonvh/blanket#231): server-side log grep, so
// an agent debugging a failure gets the matching lines instead of the
// last 50. It costs ~770 characters with its annotations;
// blanket_tasks' argument descriptions were trimmed by ~45 first.
//
// If a future addition trips TestToolListFitsContextBudget, apply the
// levers in order — trim jsonschema arg descriptions, then tool
// Description strings, then move prose into blanket_docs — and prefer
// any of those over shaving wording that an agent actually needs to call
// a tool correctly. See docs/mcp.md's "Context cost" section.
const mcpContextBudgetChars = 7000

const mcpInstructions = `blanket runs shell tasks defined by TOML task types. To author a new task type: call blanket_docs(page="authoring") for the guide, then blanket_write_task_type to save and validate it. To run it: blanket_run_task submits and waits, returning the output; blanket_submit_task queues without waiting. Either only runs once a worker is available whose tags are a superset of the type's tags -- use blanket_workers to check, blanket_launch_worker to start one. Check status with blanket_tasks(id=...); grep or page its log with blanket_task_logs.`

// buildMCPServer constructs the MCP server for s, registering only the
// tools allowed by the configured mcp.mode. This is the server every
// caller gets while auth is off.
func (s *ServerConfig) buildMCPServer() *mcp.Server {
	return s.buildMCPServerFor(auth.AuthOffPrincipal())
}

// buildMCPServerFor registers the tools both mcp.mode and p's scopes allow
// (turtlemonvh/blanket#101): mode decides which tools exist, scopes only
// narrow that. Only p's scopes are consulted, never its restrictions or
// id, so one server serves every token with the same scope set.
func (s *ServerConfig) buildMCPServerFor(p *auth.Principal) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "blanket",
		Version: s.Version,
	}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
	})

	mode := viper.GetString("mcp.mode")
	s.registerReadonlyMCPTools(srv, mode, p)
	s.registerCreateMCPTools(srv, mode, p)
	s.registerDestructiveMCPTools(srv, mode, p)
	s.registerMCPPrompts(srv, mode, p)

	return srv
}

// mcpToolScopes is which scopes see each tool (A.2); a caller needs any
// one of them. The read-side tools also answer submit, because a token can
// always read the tasks it created; their handlers filter what it sees.
// blanket_cancel_task answers submit for the same reason: a token can
// cancel its own tasks without tasks:manage.
var mcpToolScopes = map[string][]auth.Scope{
	"blanket_docs":            {auth.ScopeRead, auth.ScopeSubmit},
	"blanket_task_types":      {auth.ScopeRead, auth.ScopeSubmit},
	"blanket_tasks":           {auth.ScopeRead, auth.ScopeSubmit},
	"blanket_task_logs":       {auth.ScopeRead, auth.ScopeSubmit},
	"blanket_workers":         {auth.ScopeRead},
	"blanket_write_task_type": {auth.ScopeTypesWrite},
	"blanket_submit_task":     {auth.ScopeSubmit},
	"blanket_run_task":        {auth.ScopeSubmit},
	"blanket_launch_worker":   {auth.ScopeWorkersManage},
	"blanket_cancel_task":     {auth.ScopeTasksManage, auth.ScopeSubmit},
	"blanket_stop_worker":     {auth.ScopeWorkersManage},
}

// mcpPrincipalKey is where the caller's principal rides in the go-sdk's
// per-request TokenInfo.
const mcpPrincipalKey = "blanket.principal"

// addMCPTool registers t on srv if p may see it, wrapping h in a per-call
// scope check. The check repeats what registration decided, so a session
// that outlives a token change, or a server built for the wrong caller,
// still can't call past the caller's scopes.
func addMCPTool[In any](s *ServerConfig, srv *mcp.Server, p *auth.Principal, t *mcp.Tool, h mcp.ToolHandlerFor[In, any]) {
	need, ok := mcpToolScopes[t.Name]
	if !ok {
		panic("MCP tool " + t.Name + " has no row in mcpToolScopes")
	}
	if !hasAny(p, need) {
		return
	}
	mcp.AddTool(srv, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		caller, err := s.mcpCaller(req)
		if err != nil {
			return nil, nil, err
		}
		if !hasAny(caller, need) {
			_, msg := forbidden(need...)
			return nil, nil, errors.New(msg)
		}
		return h(ctx, req, in)
	})
}

// mcpCaller is the principal a tool call runs as: the one the HTTP
// middleware resolved, carried in TokenInfo by mcpHTTPHandler. No TokenInfo
// means an auth-off request (or an in-process test session), which is only
// acceptable while auth is actually off.
func (s *ServerConfig) mcpCaller(req *mcp.CallToolRequest) (*auth.Principal, error) {
	if req != nil && req.Extra != nil && req.Extra.TokenInfo != nil {
		if p, ok := req.Extra.TokenInfo.Extra[mcpPrincipalKey].(*auth.Principal); ok {
			return p, nil
		}
	}
	if s.AuthEnabled() {
		return nil, errors.New("authentication required")
	}
	return auth.AuthOffPrincipal(), nil
}

// closeMCPSessions ends every open MCP session and drops the per-scope
// server cache. Called when auth switches on or off: a session opened
// while auth was off was created with no TokenInfo, so go-sdk bound it to
// no user, and any token could later drive it -- against the auth-off
// server, whose tools/list shows every tool (turtlemonvh/blanket#101
// review, L4). Closing a session removes its id from the streamable
// handler, so a client holding one gets 404 and starts a new session,
// which binds to its token as usual.
//
// ServerSession.Close waits for in-flight calls (a blanket_run_task can
// wait minutes), so the sessions close in parallel and this returns after
// mcpSessionCloseWait at most. Close refuses new requests on a session
// straight away, so a session still draining can't be used either.
func (s *ServerConfig) closeMCPSessions() {
	s.mcpMu.Lock()
	servers := s.mcpServers
	s.mcpServers = nil
	s.mcpMu.Unlock()

	var wg sync.WaitGroup
	n := 0
	for _, srv := range servers {
		for sess := range srv.Sessions() {
			n++
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = sess.Close()
			}()
		}
	}
	if n == 0 {
		return
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(mcpSessionCloseWait):
		log.WithField("sessions", n).Warn("auth changed: some MCP sessions are still draining in-flight calls")
	}
	log.WithField("sessions", n).Warn("auth changed: closed every open MCP session; clients reconnect with their token")
}

// mcpSessionCloseWait bounds how long closeMCPSessions waits.
const mcpSessionCloseWait = 2 * time.Second

// mcpServerFor returns the cached MCP server for p's scope set, building it
// on first use. The auth-off principal shares one server.
func (s *ServerConfig) mcpServerFor(p *auth.Principal) *mcp.Server {
	key := "auth-off"
	if !p.AuthOff() {
		key = auth.ScopeKey(p.Scopes())
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if s.mcpServers == nil {
		s.mcpServers = map[string]*mcp.Server{}
	}
	if srv, ok := s.mcpServers[key]; ok {
		return srv
	}
	srv := s.buildMCPServerFor(p)
	s.mcpServers[key] = srv
	return srv
}

// mcpHTTPHandler returns the http.Handler to mount at /mcp, or nil if
// mcp.enabled is false.
func (s *ServerConfig) mcpHTTPHandler() http.Handler {
	if !viper.GetBool("mcp.enabled") {
		return nil
	}

	mode := viper.GetString("mcp.mode")
	fields := log.Fields{"mode": mode}
	if mode == "all" {
		log.WithFields(fields).Warn("MCP server mounted at /mcp with mode=all — this exposes task-type write and worker-launch to any host that can reach this port")
	} else {
		log.WithFields(fields).Info("MCP server mounted at /mcp")
	}

	// One handler; the server it hands each request is picked per caller
	// (decision "MCP tools/list shows only tools the caller's scopes
	// allow"). The route-table middleware has already authenticated the
	// request and put its principal on the context.
	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, ok := principalFromContext(r.Context())
		if !ok {
			p = auth.AuthOffPrincipal()
		}
		return s.mcpServerFor(p)
	}, &mcp.StreamableHTTPOptions{
		CrossOriginProtection: http.NewCrossOriginProtection(),
	})

	// With a real token, go through the SDK's bearer middleware: it is the
	// only way to put TokenInfo on the request, which is how tool calls
	// learn their caller (mcpCaller), and its UserID binds each MCP
	// session to the token that opened it, so another token can't drive
	// that session. The verifier re-checks nothing -- the route-table
	// middleware already resolved the token -- it only hands that result
	// over.
	withToken := mcpauth.RequireBearerToken(func(ctx context.Context, _ string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		p, ok := principalFromContext(ctx)
		if !ok || p.AuthOff() {
			return nil, mcpauth.ErrInvalidToken
		}
		info := &mcpauth.TokenInfo{UserID: p.Id(), Extra: map[string]any{mcpPrincipalKey: p}}
		for _, sc := range p.Scopes() {
			info.Scopes = append(info.Scopes, string(sc))
		}
		if p.Token.ExpiresTs != 0 {
			info.Expiration = time.Unix(p.Token.ExpiresTs, 0)
		}
		return info, nil
	}, &mcpauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(streamable)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := principalFromContext(r.Context()); ok && !p.AuthOff() {
			withToken.ServeHTTP(w, r)
			return
		}
		streamable.ServeHTTP(w, r)
	})
}

// The following are filled in by later tasks (10-17), one per tool tier.
// Each must check mcpModeAllows before calling mcp.AddTool.

func (s *ServerConfig) registerReadonlyMCPTools(srv *mcp.Server, mode string, p *auth.Principal) {
	if !mcpModeAllows(mode, mcpTierReadonly) {
		return
	}
	// The page list is built from docs.Keys() rather than written out
	// here. It was written out here, and it drifted: this string was
	// missing `upgrade` (added in #135) and `install` (added in #175),
	// so an agent reading the tool list could not discover two pages
	// that worked perfectly well if it happened to guess the key.
	//
	// Generating it also means adding a page to lib/docs is a one-line
	// change again, and TestMCPDocsToolListsEveryPage keeps the two from
	// separating a third time.
	addMCPTool(s, srv, p, &mcp.Tool{
		Name: "blanket_docs",
		Description: fmt.Sprintf(
			"Search blanket docs, or read a page (%s) or one section of it.",
			strings.Join(docs.Keys(), ", ")),
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpDocs)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_task_types",
		Description: "List task types, or fetch one by name for detail.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpTaskTypes)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_tasks",
		Description: "List tasks (filter by state/type), or fetch one by id with a log tail. For more log, blanket_task_logs.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpTasks)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_task_logs",
		Description: "Search or page a task log server-side: grep returns numbered matching lines with context; offset/limit count matches, or lines without grep.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpTaskLogs)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_workers",
		Description: "List workers, or fetch one by id with a log tail.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpWorkers)
}

func (s *ServerConfig) registerCreateMCPTools(srv *mcp.Server, mode string, p *auth.Principal) {
	if !mcpModeAllows(mode, mcpTierCreate) {
		return
	}
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_write_task_type",
		Description: "Validate and write a task type TOML. Refuses on any validation error; see blanket_docs(page=\"authoring\") first.",
		// Destructive, unlike the other create-tier tools: it can
		// replace an existing task type's file.
		Annotations: mcpDestructiveAnnotations,
	}, s.mcpWriteTaskType)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_submit_task",
		Description: "Submit a task of the given type. Requires an available worker whose tags superset the type's tags to run. Optional notBefore delays it once; cron makes it a recurring template instead (excl. w/ each other).",
		Annotations: mcpAdditiveAnnotations,
	}, s.mcpSubmitTask)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_run_task",
		Description: "Submit a task, wait for it, and return its state, exit code, output and parsed result in one call. Prefer this for short tasks; needs a matching worker. waitSeconds is capped by tasks.sync.maxWait.",
		Annotations: mcpAdditiveAnnotations,
	}, s.mcpRunTask)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_launch_worker",
		Description: "Launch one or more workers with given tags.",
		Annotations: mcpAdditiveAnnotations,
	}, s.mcpLaunchWorker)
}

func (s *ServerConfig) registerDestructiveMCPTools(srv *mcp.Server, mode string, p *auth.Principal) {
	if !mcpModeAllows(mode, mcpTierAll) {
		return
	}
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_cancel_task",
		Description: "Cancel a task (WAITING/SCHEDULED/RECURRING/PAUSED, or RUNNING w/ force=true), optionally delete (delete=true).",
		Annotations: mcpDestructiveAnnotations,
	}, s.mcpCancelTask)
	addMCPTool(s, srv, p, &mcp.Tool{
		Name:        "blanket_stop_worker",
		Description: "Stop a worker after its current task finishes, optionally delete record (delete=true).",
		Annotations: mcpDestructiveAnnotations,
	}, s.mcpStopWorker)
}
