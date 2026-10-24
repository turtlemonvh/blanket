package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"

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
// the measured tools/list + Instructions size (currently ~5820 chars) is
// close to this budget by design; ten tools' worth of descriptions is a
// lean surface, not accumulated bloat. It has been raised three times,
// each time for a tool surface that grew rather than for prose that
// sprawled: 4000 -> 4400 when blanket_submit_task and blanket_cancel_task
// grew notBefore/cron and wider-state descriptions
// (turtlemonvh/blanket#61's pause/resume/schedule rework), 4400 -> 5000
// for the tenth tool, blanket_run_task (turtlemonvh/blanket#27) —
// submit-and-wait is the interaction an agent actually wants, and a tool
// it can't see is worth nothing. Descriptions were trimmed first; the
// raise covers what was left. And 5000 -> 6000 for tool annotations and
// blanket_write_task_type's overwrite argument (turtlemonvh/blanket#229):
// 4986 -> 5822, about 750 of it the annotations objects, which go-sdk
// serializes in full on every tool and which carry no prose to trim.
// They are what lets a client auto-approve reads and warn before a
// destructive call.
//
// If a future addition trips TestToolListFitsContextBudget, apply the
// levers in order — trim jsonschema arg descriptions, then tool
// Description strings, then move prose into blanket_docs — and prefer
// any of those over shaving wording that an agent actually needs to call
// a tool correctly. See docs/mcp.md's "Context cost" section.
const mcpContextBudgetChars = 6000

const mcpInstructions = `blanket runs shell tasks defined by TOML task types. To author a new task type: call blanket_docs(page="authoring") for the guide, then blanket_write_task_type to save and validate it. To run it: blanket_run_task submits and waits, returning the output; blanket_submit_task queues without waiting. Either only runs once a worker is available whose tags are a superset of the type's tags -- use blanket_workers to check, blanket_launch_worker to start one. Check status and logs with blanket_tasks(id=...).`

// buildMCPServer constructs the MCP server for s, registering only the
// tools allowed by the configured mcp.mode.
func (s *ServerConfig) buildMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "blanket",
		Version: s.Version,
	}, &mcp.ServerOptions{
		Instructions: mcpInstructions,
	})

	mode := viper.GetString("mcp.mode")
	s.registerReadonlyMCPTools(srv, mode)
	s.registerCreateMCPTools(srv, mode)
	s.registerDestructiveMCPTools(srv, mode)
	s.registerMCPPrompts(srv, mode)

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

	srv := s.buildMCPServer()
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{
		CrossOriginProtection: http.NewCrossOriginProtection(),
	})
}

// The following are filled in by later tasks (10-17), one per tool tier.
// Each must check mcpModeAllows before calling mcp.AddTool.

func (s *ServerConfig) registerReadonlyMCPTools(srv *mcp.Server, mode string) {
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
	mcp.AddTool(srv, &mcp.Tool{
		Name: "blanket_docs",
		Description: fmt.Sprintf(
			"Search blanket docs, or read a page (%s) or one section of it.",
			strings.Join(docs.Keys(), ", ")),
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpDocs)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_task_types",
		Description: "List task types, or fetch one by name for detail.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpTaskTypes)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_tasks",
		Description: "List tasks (filter by state/type), or fetch one by id with a log tail.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpTasks)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_workers",
		Description: "List workers, or fetch one by id with a log tail.",
		Annotations: mcpReadOnlyAnnotations,
	}, s.mcpWorkers)
}

func (s *ServerConfig) registerCreateMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierCreate) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_write_task_type",
		Description: "Validate and write a task type TOML. Refuses on any validation error; see blanket_docs(page=\"authoring\") first.",
		// Destructive, unlike the other create-tier tools: it can
		// replace an existing task type's file.
		Annotations: mcpDestructiveAnnotations,
	}, s.mcpWriteTaskType)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_submit_task",
		Description: "Submit a task of the given type. Requires an available worker whose tags superset the type's tags to run. Optional notBefore delays it once; cron makes it a recurring template instead (excl. w/ each other).",
		Annotations: mcpAdditiveAnnotations,
	}, s.mcpSubmitTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_run_task",
		Description: "Submit a task, wait for it, and return its state, exit code, output and parsed result in one call. Prefer this for short tasks; needs a matching worker. waitSeconds is capped by tasks.sync.maxWait.",
		Annotations: mcpAdditiveAnnotations,
	}, s.mcpRunTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_launch_worker",
		Description: "Launch one or more workers with given tags.",
		Annotations: mcpAdditiveAnnotations,
	}, s.mcpLaunchWorker)
}

func (s *ServerConfig) registerDestructiveMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierAll) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_cancel_task",
		Description: "Cancel a task (WAITING/SCHEDULED/RECURRING/PAUSED, or RUNNING w/ force=true), optionally delete (delete=true).",
		Annotations: mcpDestructiveAnnotations,
	}, s.mcpCancelTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_stop_worker",
		Description: "Stop a worker after its current task finishes, optionally delete record (delete=true).",
		Annotations: mcpDestructiveAnnotations,
	}, s.mcpStopWorker)
}
