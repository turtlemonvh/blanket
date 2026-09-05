package server

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
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

// mcpContextBudgetChars is a design tripwire, not a hard protocol limit —
// the measured tools/list + Instructions size (currently ~4320 chars,
// bumped from 4000 when blanket_submit_task and blanket_cancel_task grew
// notBefore/cron and wider-state descriptions for turtlemonvh/blanket#61's
// pause/resume/schedule rework) is close to this budget by design; nine
// tools' worth of descriptions is a lean surface, not accumulated bloat.
// If a future addition trips TestToolListFitsContextBudget, prefer moving
// prose into blanket_docs over shaving wording that an agent actually
// needs to call a tool correctly — see
// docs/superpowers/plans/2026-09-01-blanket-mcp-interface.md's Context
// budget section for the full lever ordering.
const mcpContextBudgetChars = 4400

const mcpInstructions = `blanket runs shell tasks defined by TOML task types. To author a new task type: call blanket_docs(page="authoring") for the guide, then blanket_write_task_type to save and validate it. To run it: blanket_submit_task queues a task of that type, but it only runs once a worker is available whose tags are a superset of the type's tags -- use blanket_workers to check, blanket_launch_worker to start one. Check status and logs with blanket_tasks(id=...).`

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
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_docs",
		Description: "Fetch a blanket doc page (overview, authoring, schema, tags, usage, api, flow). Read 'authoring' before writing a task type.",
	}, s.mcpDocs)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_task_types",
		Description: "List task types, or fetch one by name for detail.",
	}, s.mcpTaskTypes)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_tasks",
		Description: "List tasks (filter by state/type), or fetch one by id with a log tail.",
	}, s.mcpTasks)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_workers",
		Description: "List workers, or fetch one by id with a log tail.",
	}, s.mcpWorkers)
}

func (s *ServerConfig) registerCreateMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierCreate) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_write_task_type",
		Description: "Validate and write a task type TOML. Refuses on any validation error; see blanket_docs(page=\"authoring\") first.",
	}, s.mcpWriteTaskType)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_submit_task",
		Description: "Submit a task of the given type. Requires an available worker whose tags superset the type's tags to run. Optional notBefore delays it once; cron makes it a recurring template instead (excl. w/ each other).",
	}, s.mcpSubmitTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_launch_worker",
		Description: "Launch one or more workers with given tags.",
	}, s.mcpLaunchWorker)
}

func (s *ServerConfig) registerDestructiveMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierAll) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_cancel_task",
		Description: "Cancel a task (WAITING/SCHEDULED/RECURRING/PAUSED, or RUNNING w/ force=true), optionally delete (delete=true).",
	}, s.mcpCancelTask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "blanket_stop_worker",
		Description: "Stop a worker after its current task finishes, optionally delete record (delete=true).",
	}, s.mcpStopWorker)
}
