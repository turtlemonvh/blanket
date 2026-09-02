package server

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

const mcpContextBudgetChars = 4000

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
}

func (s *ServerConfig) registerCreateMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierCreate) {
		return
	}
}

func (s *ServerConfig) registerDestructiveMCPTools(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierAll) {
		return
	}
}
