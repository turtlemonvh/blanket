package server

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectMCPTestClient builds the MCP server for the current mcp.mode
// and returns a connected client session plus a cleanup func.
func connectMCPTestClient(t *testing.T, s *ServerConfig) (*mcp.ClientSession, func()) {
	t.Helper()
	srv := s.buildMCPServer()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	return clientSession, func() {
		clientSession.Close()
		serverSession.Wait()
	}
}

func listPromptNames(t *testing.T, cs *mcp.ClientSession) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	res, err := cs.ListPrompts(context.Background(), &mcp.ListPromptsParams{})
	if err != nil {
		// A server with no prompts doesn't advertise the capability;
		// that counts as an empty list.
		return got
	}
	for _, p := range res.Prompts {
		got[p.Name] = true
	}
	return got
}

func TestMCPPromptModeGating(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	defer viper.Set("mcp.mode", nil)

	want := map[string]bool{"readonly": false, "create": true, "all": true}
	for mode, listed := range want {
		viper.Set("mcp.mode", mode)
		cs, done := connectMCPTestClient(t, s)
		got := listPromptNames(t, cs)
		assert.Equal(t, listed, got["new_task_type"], "mode %q: new_task_type listed", mode)
		done()
	}
}

// getNewTaskTypePrompt fetches new_task_type with args and returns its
// single text message.
func getNewTaskTypePrompt(t *testing.T, args map[string]string) string {
	t.Helper()
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.mode", "create")
	defer viper.Set("mcp.mode", nil)

	cs, done := connectMCPTestClient(t, s)
	defer done()

	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      "new_task_type",
		Arguments: args,
	})
	require.NoError(t, err)
	require.Len(t, res.Messages, 1)
	assert.Equal(t, mcp.Role("user"), res.Messages[0].Role)

	text, ok := res.Messages[0].Content.(*mcp.TextContent)
	require.True(t, ok, "prompt message should be text, got %T", res.Messages[0].Content)
	assert.NotContains(t, text.Text, "%!", "template formatting error in prompt text")
	return text.Text
}

// assertWorkflowTools checks that every step of the workflow names the
// tool it uses, whatever the description argument was.
func assertWorkflowTools(t *testing.T, text string) {
	t.Helper()
	for _, step := range []string{
		`blanket_docs(page="authoring")`,
		"blanket_task_types",
		"blanket_write_task_type",
		"blanket_workers",
		"blanket_launch_worker",
		"blanket_run_task",
	} {
		assert.Contains(t, text, step)
	}
}

func TestMCPNewTaskTypePrompt_FillsDescription(t *testing.T) {
	text := getNewTaskTypePrompt(t, map[string]string{"description": "back up ~/notes to a tarball"})
	assert.Contains(t, text, `"back up ~/notes to a tarball"`)
	// A description may arrive truncated (clients that split arguments
	// on whitespace), so the text must say to check it and ask.
	assert.Contains(t, text, "may be incomplete")
	assert.Contains(t, text, "ask the user")
	assert.NotContains(t, text, "No description was passed")
	assertWorkflowTools(t, text)
}

func TestMCPNewTaskTypePrompt_NoDescription(t *testing.T) {
	for name, args := range map[string]map[string]string{
		"missing": nil,
		"blank":   {"description": "   "},
	} {
		t.Run(name, func(t *testing.T) {
			text := getNewTaskTypePrompt(t, args)
			assert.Contains(t, text, "No description was passed")
			assert.Contains(t, text, "conversation")
			assert.Contains(t, text, "ask the user what the task type should run")
			assert.NotContains(t, text, "described the task as")
			assertWorkflowTools(t, text)
		})
	}
}

func TestMCPNewTaskTypePrompt_DescriptionOptional(t *testing.T) {
	s, cleanup := NewTestServer()
	defer cleanup()
	viper.Set("mcp.mode", "create")
	defer viper.Set("mcp.mode", nil)

	cs, done := connectMCPTestClient(t, s)
	defer done()

	res, err := cs.ListPrompts(context.Background(), &mcp.ListPromptsParams{})
	require.NoError(t, err)
	for _, p := range res.Prompts {
		if p.Name != "new_task_type" {
			continue
		}
		for _, a := range p.Arguments {
			assert.False(t, a.Required, "argument %q must be optional", a.Name)
		}
		return
	}
	t.Fatal("new_task_type not listed")
}
