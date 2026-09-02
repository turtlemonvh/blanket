package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/turtlemonvh/blanket/docs"
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
