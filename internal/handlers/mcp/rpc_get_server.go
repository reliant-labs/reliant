package mcp

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetServer returns detailed information about a specific MCP server
func (s *Service) GetServer(
	ctx context.Context,
	req *connect.Request[reliantv1.GetServerRequest],
) (*connect.Response[reliantv1.GetServerResponse], error) {
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

	if s.deps.Router == nil {
		return nil, svcerr.WithMessage(svcerr.ErrNotFound, "MCP not available")
	}

	scoped, configured := scopedServers[name]
	if !configured {
		return nil, svcerr.NotFound("server")
	}

	server := mcpServerFromConfig(name, scoped)
	status := s.serverStatus(ctx, userID, checkout)
	if !scoped.Config.IsEnabled() {
		// A disabled server reports why it last failed, if it did.
		server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED
		if entry, ok := status.entry(name); ok && entry.LastError != "" {
			lastErr := entry.LastError
			server.LastError = &lastErr
		}
		return connect.NewResponse(&reliantv1.GetServerResponse{Server: server}), nil
	}
	applyServerStatus(server, status)

	return connect.NewResponse(&reliantv1.GetServerResponse{Server: server}), nil
}
