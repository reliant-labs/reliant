package tools

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// argumentRecordingRuntime captures the argument map handed to the MCP server.
type argumentRecordingRuntime struct {
	arguments map[string]interface{}
}

func (r *argumentRecordingRuntime) EnsureProjectServersLoaded(context.Context, string) *mcp.ProjectServerLoadResult {
	return &mcp.ProjectServerLoadResult{}
}
func (r *argumentRecordingRuntime) ListProjectTools(string) (map[string][]mcp.Tool, error) {
	return map[string][]mcp.Tool{}, nil
}
func (r *argumentRecordingRuntime) ListAllTools() (map[string][]mcp.Tool, error) {
	return map[string][]mcp.Tool{}, nil
}
func (r *argumentRecordingRuntime) ProjectCallTool(_, _, _, _ string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	r.arguments = arguments
	return &mcp.ToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "ok"}}}, nil
}
func (r *argumentRecordingRuntime) CallTool(_, _, _ string, arguments map[string]interface{}) (*mcp.ToolResult, error) {
	r.arguments = arguments
	return &mcp.ToolResult{Content: []mcp.ToolContent{{Type: "text", Text: "ok"}}}, nil
}

// An MCP server reads a JSON "" and a missing key as two different requests:
// chrome-devtools resolves "" against its own cwd and denies the write, while a
// missing key means "return the output inline". The adapter must therefore
// forward exactly the keys the model produced — never materialize a zero value
// for a key the model left out, and never drop a zero value the model chose.
func TestMCPToolAdapter_ForwardsProvidedKeysAndOnlyProvidedKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   string
		present map[string]interface{}
		absent  []string
	}{
		{
			name:    "omitted optional parameters are absent from the payload",
			input:   `{"function":"() => document.title"}`,
			present: map[string]interface{}{"function": "() => document.title"},
			absent:  []string{"filePath", "dialogAction", "args"},
		},
		{
			name:    "an explicitly empty string is still sent",
			input:   `{"function":"() => 1","filePath":""}`,
			present: map[string]interface{}{"filePath": ""},
		},
		{
			name:    "an explicit false is still sent",
			input:   `{"verbose":false}`,
			present: map[string]interface{}{"verbose": false},
		},
		{
			name:    "an explicit zero is still sent",
			input:   `{"timeout":0}`,
			present: map[string]interface{}{"timeout": float64(0)},
		},
		{
			name:    "an explicit null is still sent",
			input:   `{"filePath":null}`,
			present: map[string]interface{}{"filePath": nil},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &argumentRecordingRuntime{}
			adapter, err := NewMCPToolAdapter("chrome-devtools", mcp.Tool{Name: "evaluate_script"})
			if err != nil {
				t.Fatalf("NewMCPToolAdapter: %v", err)
			}

			toolCtx := rctx.NewToolContext(context.Background(), "chat-1", "thread-a", nil, nil).WithMCP(runtime)
			if _, err := adapter.Run(toolCtx, ToolCall{ID: "1", Name: adapter.Name(), Input: tc.input}); err != nil {
				t.Fatalf("Run: %v", err)
			}

			for key, want := range tc.present {
				got, ok := runtime.arguments[key]
				if !ok {
					t.Fatalf("key %q must be present in the payload, got %#v", key, runtime.arguments)
				}
				if got != want {
					t.Fatalf("key %q = %#v, want %#v", key, got, want)
				}
			}
			for _, key := range tc.absent {
				if _, ok := runtime.arguments[key]; ok {
					t.Fatalf("key %q must NOT be in the payload, got %#v", key, runtime.arguments)
				}
			}
		})
	}
}
