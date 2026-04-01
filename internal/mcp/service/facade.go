package service

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/reliant-labs/reliant/internal/mcp/runtime"
)

// MCPService is the app-facing MCP facade for runtime session operations.
type MCPService struct {
	toolCalls *ToolCallService
}

func NewMCPService(toolCalls *ToolCallService) *MCPService {
	if toolCalls == nil {
		toolCalls = NewToolCallService(nil)
	}
	return &MCPService{toolCalls: toolCalls}
}

func (s *MCPService) CallTool(ctx context.Context, session runtime.Session, serverName, toolName string, arguments map[string]interface{}) (*mcp.CallToolResult, error) {
	if session == nil {
		return nil, fmt.Errorf("not connected")
	}
	result, _, err := s.toolCalls.CallToolWithCompatibility(ctx, session, buildCallRequest(serverName, toolName, arguments))
	return result, err
}

func (s *MCPService) ListTools(ctx context.Context, session runtime.Session) (*mcp.ListToolsResult, error) {
	if session == nil {
		return nil, fmt.Errorf("not connected")
	}
	return session.ListTools(ctx, &mcp.ListToolsParams{})
}

func (s *MCPService) ListResources(ctx context.Context, session runtime.Session) (*mcp.ListResourcesResult, error) {
	if session == nil {
		return nil, fmt.Errorf("not connected")
	}
	return session.ListResources(ctx, &mcp.ListResourcesParams{})
}

func (s *MCPService) ReadResource(ctx context.Context, session runtime.Session, uri string) (*mcp.ReadResourceResult, error) {
	if session == nil {
		return nil, fmt.Errorf("not connected")
	}
	return session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
}

func (s *MCPService) ListPrompts(ctx context.Context, session runtime.Session) (*mcp.ListPromptsResult, error) {
	if session == nil {
		return nil, fmt.Errorf("not connected")
	}
	return session.ListPrompts(ctx, &mcp.ListPromptsParams{})
}

func (s *MCPService) GetPrompt(ctx context.Context, session runtime.Session, name string, arguments map[string]interface{}) (*mcp.GetPromptResult, error) {
	if session == nil {
		return nil, fmt.Errorf("not connected")
	}
	return session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      name,
		Arguments: ToPromptArguments(arguments),
	})
}
