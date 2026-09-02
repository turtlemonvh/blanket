package server

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/turtlemonvh/blanket/docs"
	"github.com/turtlemonvh/blanket/lib/database"
	"github.com/turtlemonvh/blanket/lib/objectid"
	"github.com/turtlemonvh/blanket/tasks"
)

// textResult wraps s as a successful single-text-block tool result. Every
// MCP tool handler in this file returns plain text, not JSON — see the
// design plan's "Context budget" section for why.
func textResult(s string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}, nil, nil
}

type blanketDocsArgs struct {
	Page string `json:"page" jsonschema:"doc page to fetch: overview, authoring, schema, tags, usage, api, or flow"`
}

func (s *ServerConfig) mcpDocs(ctx context.Context, req *mcp.CallToolRequest, args blanketDocsArgs) (*mcp.CallToolResult, any, error) {
	content, err := docs.Page(args.Page)
	if err != nil {
		return nil, nil, err
	}
	return textResult(content)
}

type blanketTaskTypesArgs struct {
	Name string `json:"name,omitempty" jsonschema:"task type name; omit to list all"`
}

func (s *ServerConfig) mcpTaskTypes(ctx context.Context, req *mcp.CallToolRequest, args blanketTaskTypesArgs) (*mcp.CallToolResult, any, error) {
	if args.Name != "" {
		tt, err := tasks.FetchTaskType(args.Name)
		if err != nil {
			return nil, nil, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "name: %s\n", tt.GetName())
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(tt.Config.GetStringSlice("tags"), ", "))
		fmt.Fprintf(&b, "description: %s\n", tt.GetDescription())
		fmt.Fprintf(&b, "documentation: %s\n", tt.GetDocumentation())
		fmt.Fprintf(&b, "required env: %s\n", strings.Join(tt.EnvNames("required"), ", "))
		fmt.Fprintf(&b, "default env: %s\n", strings.Join(tt.EnvNames("default"), ", "))
		return textResult(b.String())
	}

	tts, err := tasks.ReadTypes()
	if err != nil {
		return nil, nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-30s %s\n", "NAME", "TAGS", "DESCRIPTION")
	for _, tt := range tts {
		fmt.Fprintf(&b, "%-24s %-30s %s\n", tt.GetName(), strings.Join(tt.Config.GetStringSlice("tags"), ","), tt.GetDescription())
	}
	return textResult(b.String())
}

const (
	mcpDefaultLogLines  = 50
	mcpDefaultTaskLimit = 20
	mcpMaxTaskLimit     = 100
)

// clampLogLines applies the mcp.maxLogLines guard: n defaults to
// mcpDefaultLogLines when unset, and is capped at mcp.maxLogLines
// regardless of what the caller asked for. A worker or task log can be
// tens of megabytes, so this is a hard context guard, not a preference.
func clampLogLines(n int) int {
	if n <= 0 {
		n = mcpDefaultLogLines
	}
	if max := viper.GetInt("mcp.maxLogLines"); max > 0 && n > max {
		n = max
	}
	return n
}

type blanketTasksArgs struct {
	Id       string   `json:"id,omitempty" jsonschema:"task id; if set, returns detail plus a log tail instead of a list"`
	States   []string `json:"states,omitempty" jsonschema:"filter by task state (WAITING, CLAIMED, RUNNING, SUCCESS, ERROR, STOPPED, TIMEDOUT)"`
	Types    []string `json:"types,omitempty" jsonschema:"filter by task type name"`
	Limit    int      `json:"limit,omitempty" jsonschema:"max tasks to list, default 20, max 100"`
	LogLines int      `json:"log_lines,omitempty" jsonschema:"log tail lines when id is set, default 50"`
}

func (s *ServerConfig) mcpTasks(ctx context.Context, req *mcp.CallToolRequest, args blanketTasksArgs) (*mcp.CallToolResult, any, error) {
	if args.Id != "" {
		if !objectid.IsObjectIdHex(args.Id) {
			return nil, nil, fmt.Errorf("%q is not a valid task id", args.Id)
		}
		taskId := objectid.ObjectIdHex(args.Id)
		task, err := s.DB.GetTask(taskId)
		if err != nil {
			return nil, nil, err
		}

		logLines := clampLogLines(args.LogLines)
		stdoutPath := path.Join(task.ResultDir, "blanket.stdout.log")
		logTail, _ := tailLines(stdoutPath, logLines)

		var b strings.Builder
		fmt.Fprintf(&b, "id: %s\n", task.Id.Hex())
		fmt.Fprintf(&b, "type: %s\n", task.TypeId)
		fmt.Fprintf(&b, "state: %s\n", task.State)
		fmt.Fprintf(&b, "progress: %d%%\n", task.Progress)
		fmt.Fprintf(&b, "workerId: %s\n", task.WorkerId.Hex())
		fmt.Fprintf(&b, "tags: %s\n", strings.Join(task.Tags, ", "))
		fmt.Fprintf(&b, "createdTs: %d\n", task.CreatedTs)
		fmt.Fprintf(&b, "lastUpdatedTs: %d\n", task.LastUpdatedTs)
		fmt.Fprintf(&b, "\n--- log tail (%d lines) ---\n%s", logLines, logTail)
		return textResult(b.String())
	}

	limit := args.Limit
	if limit <= 0 {
		limit = mcpDefaultTaskLimit
	}
	if limit > mcpMaxTaskLimit {
		limit = mcpMaxTaskLimit
	}

	tc := &database.TaskSearchConf{
		Limit:             limit,
		AllowedTaskStates: map[string]bool{},
		AllowedTaskTypes:  map[string]bool{},
		SmallestId:        objectid.NewObjectIdWithTime(time.Unix(0, 0)),
		LargestId:         objectid.NewObjectIdWithTime(time.Unix(database.FAR_FUTURE_SECONDS, 0)),
	}
	for _, st := range args.States {
		tc.AllowedTaskStates[st] = true
	}
	for _, ty := range args.Types {
		tc.AllowedTaskTypes[ty] = true
	}

	result, _, err := s.DB.GetTasks(tc)
	if err != nil {
		return nil, nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-16s %-8s %s\n", "ID", "TYPE", "STATE", "PROGRESS")
	for _, t := range result {
		fmt.Fprintf(&b, "%-24s %-16s %-8s %d%%\n", t.Id.Hex(), t.TypeId, t.State, t.Progress)
	}
	return textResult(b.String())
}
