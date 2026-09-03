package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// sessionRecordingRuntime records the session key each call arrived with.
type sessionRecordingRuntime struct {
	sessions []string
}

func (r *sessionRecordingRuntime) EnsureProjectServersLoaded(context.Context, string) *mcp.ProjectServerLoadResult {
	return &mcp.ProjectServerLoadResult{}
}
func (r *sessionRecordingRuntime) ListProjectTools(string) (map[string][]mcp.Tool, error) {
	return map[string][]mcp.Tool{}, nil
}
func (r *sessionRecordingRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return map[string][]mcp.Tool{}, nil
}

func (r *sessionRecordingRuntime) ProjectCallTool(session, _, _, _ string, _ map[string]interface{}) (*mcp.ToolResult, error) {
	r.sessions = append(r.sessions, session)
	return &mcp.ToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "ok"}}}, nil
}

func (r *sessionRecordingRuntime) CallTool(session, _, _ string, _ map[string]interface{}) (*mcp.ToolResult, error) {
	r.sessions = append(r.sessions, session)
	return &mcp.ToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "ok"}}}, nil
}

// The isolation key that keeps two fan-out threads off each other's browser page
// only works if the adapter actually hands the thread down. It is the THREAD,
// not the chat: the engine fans several threads out inside one chat, and those
// siblings are exactly the callers that were clobbering each other.
func TestMCPToolAdapter_PassesTheAgentThreadAsTheSessionKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		chatID string
		thread string
		want   string
	}{
		{name: "thread wins over chat", chatID: "chat-1", thread: "thread-a", want: "thread-a"},
		{name: "chat is the fallback outside a workflow thread", chatID: "chat-1", thread: "", want: "chat-1"},
		{name: "no identity at all", chatID: "", thread: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, projectPath := range []string{"", "/tmp/project"} {
				runtime := &sessionRecordingRuntime{}
				adapter, err := NewMCPToolAdapter("chrome-devtools", mcp.Tool{Name: "select_page"})
				if err != nil {
					t.Fatalf("NewMCPToolAdapter: %v", err)
				}
				adapter.projectPath = projectPath

				toolCtx := rctx.NewToolContext(context.Background(), tc.chatID, tc.thread, nil, nil).WithMCP(runtime)
				if _, err := adapter.Run(toolCtx, ToolCall{ID: "1", Name: adapter.Name()}); err != nil {
					t.Fatalf("Run: %v", err)
				}

				if len(runtime.sessions) != 1 {
					t.Fatalf("expected exactly one MCP call, got %d", len(runtime.sessions))
				}
				if runtime.sessions[0] != tc.want {
					t.Fatalf("projectPath=%q: session key = %q, want %q", projectPath, runtime.sessions[0], tc.want)
				}
			}
		})
	}
}
