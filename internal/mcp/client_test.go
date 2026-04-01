package mcp

import (
	"context"
	"errors"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	mcpservice "github.com/reliant-labs/reliant/internal/mcp/service"
)

type scriptedToolCaller struct {
	calls   []*sdkmcp.CallToolParams
	results []*sdkmcp.CallToolResult
	errors  []error
}

func (s *scriptedToolCaller) CallTool(ctx context.Context, params *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	_ = ctx
	s.calls = append(s.calls, params)
	idx := len(s.calls) - 1

	var res *sdkmcp.CallToolResult
	if idx < len(s.results) {
		res = s.results[idx]
	}
	var err error
	if idx < len(s.errors) {
		err = s.errors[idx]
	}
	if res == nil {
		res = &sdkmcp.CallToolResult{}
	}
	return res, err
}

func TestNormalizeToolArguments(t *testing.T) {
	got := normalizeToolArguments(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("expected empty non-nil map, got %#v", got)
	}

	args := map[string]interface{}{"foo": "bar"}
	got = normalizeToolArguments(args)
	if got["foo"] != "bar" {
		t.Fatalf("expected map passthrough, got %#v", got)
	}
}

func TestCallToolWithCompatibility(t *testing.T) {
	t.Run("successful direct call", func(t *testing.T) {
		caller := &scriptedToolCaller{results: []*sdkmcp.CallToolResult{{}}}
		result, err := callToolWithCompatibility(context.Background(), caller, nil, "server", "tool", map[string]interface{}{"foo": "bar"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result == nil {
			t.Fatal("expected result")
		}
		if len(caller.calls) != 1 {
			t.Fatalf("expected 1 call got %d", len(caller.calls))
		}
		argMap, ok := caller.calls[0].Arguments.(map[string]interface{})
		if !ok || argMap["foo"] != "bar" {
			t.Fatalf("unexpected arguments: %#v", caller.calls[0].Arguments)
		}
	})

	t.Run("retries with compatibility envelope", func(t *testing.T) {
		caller := &scriptedToolCaller{
			results: []*sdkmcp.CallToolResult{nil, nil, &sdkmcp.CallToolResult{}},
			errors: []error{
				errors.New("MCP error: Invalid parameters: path [\"params\",\"application/json\"] Required"),
				errors.New("MCP error: Invalid parameters: path [\"params\",\"application/json\"] Required"),
				nil,
			},
		}
		svc := mcpservice.NewToolCallService(nil)
		_, err := callToolWithCompatibility(context.Background(), caller, svc, "server", "tool", map[string]interface{}{"foo": "bar"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(caller.calls) != 3 {
			t.Fatalf("expected 3 calls got %d", len(caller.calls))
		}
		lastArgs, ok := caller.calls[2].Arguments.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map args, got %T", caller.calls[2].Arguments)
		}
		paramsObj, ok := lastArgs["params"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected params wrapper, got %#v", lastArgs)
		}
		if _, ok := paramsObj["application/json"]; !ok {
			t.Fatalf("expected params.application/json wrapper, got %#v", lastArgs)
		}
	})
}
