package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP prompts are user-invoked templates (Claude Code lists them as
// slash commands, e.g. /blanket:new_task_type). They are not in
// tools/list, so they don't count against mcpContextBudgetChars; a
// client only fetches a prompt's text when the user picks it.

// newTaskTypePromptTemplate is the authoring workflow the
// blanket-task-type skill used to carry (turtlemonvh/blanket#232). It
// lives here so any MCP client gets it without a repo checkout. %s is
// the task paragraph: newTaskTypeTaskGiven or newTaskTypeTaskMissing.
const newTaskTypePromptTemplate = `Create a blanket task type.

%s

Work through these steps in order, using the blanket MCP tools:

1. Read the authoring guide: call blanket_docs(page="authoring"). It covers the TOML schema, the {{.VAR}} vs $VAR distinction, namespaced tags, and sizing. Call blanket_task_types to see existing types worth copying a pattern from and the tags already in use.
2. Draft the TOML. Set description and documentation, namespaced tags that reuse existing ones where they fit, a templated command, and a small environment table. Aim for 2-5 inputs: only make something an input if it genuinely varies between submissions.
3. Save it with blanket_write_task_type. It validates first and refuses on any error. Fix every error and resubmit. For each warning, either fix it or state a specific reason it is expected.
4. Make sure it can run: a task only runs on a worker whose tags are a superset of the type's tags. Check blanket_workers for a matching worker; if there is none, start one with blanket_launch_worker using the type's tags.
5. Smoke-test it with blanket_run_task and confirm the task reaches SUCCESS. Validation catches structural problems, not runtime ones (a missing binary, a typo'd flag). If it fails, read the output, fix the TOML, and repeat from step 3.

Finish by reporting the type's name, the tags chosen and why, any warnings left in place and why, and the smoke-test result.`

// Clients pass prompt arguments differently, and some (Claude Code
// among them) split them on whitespace, so a multi-word description
// can arrive as its first word or not at all. The description is
// therefore optional, and the text treats it as a hint to check
// against the conversation rather than the whole spec.
const newTaskTypeTaskGiven = `The user described the task as: "%s"

This description may be incomplete: some clients pass only the first word of a prompt argument. Check it against the conversation so far. If it is still unclear what the task type should run, ask the user before drafting anything.`

const newTaskTypeTaskMissing = `No description was passed with this prompt. If the conversation so far makes clear what the task type should run, use that. Otherwise, ask the user what the task type should run before drafting anything.`

// registerMCPPrompts adds the prompts allowed by mode. new_task_type
// drives blanket_write_task_type, blanket_launch_worker and
// blanket_run_task, so it is create-tier: offering it in readonly mode
// would hand the agent a workflow whose tools aren't registered.
func (s *ServerConfig) registerMCPPrompts(srv *mcp.Server, mode string) {
	if !mcpModeAllows(mode, mcpTierCreate) {
		return
	}
	srv.AddPrompt(&mcp.Prompt{
		Name:        "new_task_type",
		Title:       "New blanket task type",
		Description: "Author, validate, and smoke-test a new blanket task type from a description of what it should run.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "description",
			Description: "Optional: what the task should run, e.g. \"back up a Postgres database to S3\". Some clients split arguments on whitespace, so the prompt also draws on the conversation or asks.",
			Required:    false,
		}},
	}, mcpNewTaskTypePrompt)
}

func mcpNewTaskTypePrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	description := strings.TrimSpace(req.Params.Arguments["description"])
	task := newTaskTypeTaskMissing
	if description != "" {
		task = fmt.Sprintf(newTaskTypeTaskGiven, description)
	}
	return &mcp.GetPromptResult{
		Description: "Author a blanket task type",
		Messages: []*mcp.PromptMessage{{
			Role:    "user",
			Content: &mcp.TextContent{Text: fmt.Sprintf(newTaskTypePromptTemplate, task)},
		}},
	}, nil
}
