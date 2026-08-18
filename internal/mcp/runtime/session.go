// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package runtime

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Session abstracts MCP SDK session operations used by Reliant.
type Session interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	ListResources(ctx context.Context, params *mcp.ListResourcesParams) (*mcp.ListResourcesResult, error)
	ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error)
	ListPrompts(ctx context.Context, params *mcp.ListPromptsParams) (*mcp.ListPromptsResult, error)
	GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error)
	Close() error
}

type sdkSession struct {
	inner *mcp.ClientSession
}

func WrapSDKSession(inner *mcp.ClientSession) Session {
	if inner == nil {
		return nil
	}
	return &sdkSession{inner: inner}
}

func (s *sdkSession) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return s.inner.CallTool(ctx, params)
}

func (s *sdkSession) ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return s.inner.ListTools(ctx, params)
}

func (s *sdkSession) ListResources(ctx context.Context, params *mcp.ListResourcesParams) (*mcp.ListResourcesResult, error) {
	return s.inner.ListResources(ctx, params)
}

func (s *sdkSession) ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	return s.inner.ReadResource(ctx, params)
}

func (s *sdkSession) ListPrompts(ctx context.Context, params *mcp.ListPromptsParams) (*mcp.ListPromptsResult, error) {
	return s.inner.ListPrompts(ctx, params)
}

func (s *sdkSession) GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	return s.inner.GetPrompt(ctx, params)
}

func (s *sdkSession) Close() error {
	return s.inner.Close()
}
