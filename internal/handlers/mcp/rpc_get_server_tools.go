package mcp

import (
	"context"
	"sort"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetServerTools returns the list of tools exposed by a specific MCP server.
func (s *Service) GetServerTools(
	ctx context.Context,
	req *connect.Request[reliantv1.GetServerToolsRequest],
) (*connect.Response[reliantv1.GetServerToolsResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)
	checkout, err := s.projectCheckout(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	name := req.Msg.Name
	if name == "" {
		return nil, svcerr.InvalidArgument("server name is required")
	}

	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	scoped, configured := scopedServers[name]
	if !configured {
		return nil, svcerr.NotFound("server")
	}

	if !scoped.Config.IsEnabled() {
		return connect.NewResponse(&reliantv1.GetServerToolsResponse{Tools: []*reliantv1.MCPTool{}, Total: 0}), nil
	}

	if s.deps.Router == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "MCP not available")
	}

	// A server the daemon does not have has no tools to list.
	if _, ok := s.serverStatus(ctx, userID, checkout).entry(name); !ok {
		return connect.NewResponse(&reliantv1.GetServerToolsResponse{Tools: []*reliantv1.MCPTool{}, Total: 0}), nil
	}

	// The tools themselves are a live question for the server; ask within
	// the request's own budget.
	tools, err := s.mcpProxy(userID, checkout).listTools(ctx, checkout.Path, name)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(withDaemonText("failed to list tools", err)))
	}

	respTools := make([]*reliantv1.MCPTool, 0, len(tools))
	for _, tool := range tools {
		respTools = append(respTools, &reliantv1.MCPTool{
			Name:        tool.Name,
			Description: tool.Description,
		})
	}

	sort.Slice(respTools, func(i, j int) bool {
		return respTools[i].Name < respTools[j].Name
	})

	return connect.NewResponse(&reliantv1.GetServerToolsResponse{
		Tools: respTools,
		Total: int32(len(respTools)),
	}), nil
}
