package service

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/reliant-labs/reliant/internal/mcp/compat"
	"github.com/reliant-labs/reliant/internal/mcp/telemetry"
)

// ToolExecutor is the minimal execution dependency required for MCP tool calls.
type ToolExecutor interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

// ToolCallService orchestrates compatibility attempts for tool execution.
type ToolCallService struct {
	preferences *compat.PreferenceStore
}

func NewToolCallService(preferences *compat.PreferenceStore) *ToolCallService {
	if preferences == nil {
		preferences = compat.NewPreferenceStore()
	}
	return &ToolCallService{preferences: preferences}
}

func (s *ToolCallService) CallToolWithCompatibility(ctx context.Context, executor ToolExecutor, req compat.CallRequest) (*mcp.CallToolResult, compat.EnvelopeName, error) {
	preferred := s.preferences.Get(req.ServerName, req.ToolName)
	attempts := compat.BuildAttemptPlan(req.Arguments, preferred)
	if len(attempts) == 0 {
		attempts = []compat.Attempt{{Name: compat.EnvelopeDirect, Payload: map[string]interface{}{}}}
	}

	params := &mcp.CallToolParams{Name: req.ToolName}
	var lastErr error

	for i, attempt := range attempts {
		params.Arguments = attempt.Payload
		result, err := executor.CallTool(ctx, params)
		if err == nil {
			s.preferences.Set(req.ServerName, req.ToolName, attempt.Name)
			if i > 0 {
				telemetry.LogCallSuccess(req.ServerName, req.ToolName, i, len(attempts), attempt.Name)
			}
			return result, attempt.Name, nil
		}

		lastErr = err
		kind := compat.ClassifyError(err)
		next := compat.EnvelopeName("")
		if i+1 < len(attempts) {
			next = attempts[i+1].Name
		}
		telemetry.LogCallAttempt(req.ServerName, req.ToolName, i, len(attempts), attempt, err, kind, next)

		if !compat.ShouldRetry(kind, i, len(attempts)) {
			break
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unknown MCP tool call failure")
	}
	return nil, "", fmt.Errorf("failed to call tool %s: %w", req.ToolName, lastErr)
}
