package service

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/reliant-labs/reliant/internal/mcp/compat"
)

type fakeExecutor struct {
	calls []*mcp.CallToolParams
	errs  []error
}

func (f *fakeExecutor) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	_ = ctx
	f.calls = append(f.calls, params)
	idx := len(f.calls) - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	return &mcp.CallToolResult{}, nil
}

func TestCallToolWithCompatibility(t *testing.T) {
	svc := NewToolCallService(nil)
	exec := &fakeExecutor{errs: []error{
		errors.New("MCP error: Invalid parameters: path [\"params\"] Required"),
		nil,
	}}

	_, env, err := svc.CallToolWithCompatibility(context.Background(), exec, compat.CallRequest{
		ServerName: "server",
		ToolName:   "tool",
		Arguments:  map[string]interface{}{"foo": "bar"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env != compat.EnvelopeParams {
		t.Fatalf("expected params envelope got %s", env)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("expected 2 calls got %d", len(exec.calls))
	}
}
