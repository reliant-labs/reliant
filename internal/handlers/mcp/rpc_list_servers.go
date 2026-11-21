package mcp

import (
	"context"
	"sort"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ============================================================================
// RPC Methods
// ============================================================================

// ListServers returns all configured MCP servers with their status.
//
// The servers and their configuration come from the database; their runtime
// status is the daemon's last report (see Service.serverStatus), so the
// list renders without waiting on the user's machine.
func (s *Service) ListServers(
	ctx context.Context,
	req *connect.Request[reliantv1.ListServersRequest],
) (*connect.Response[reliantv1.ListServersResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)
	checkout, err := s.projectCheckout(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	// Read merged scoped config to get all configured servers
	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read MCP configuration"))
	}

	// Check if MCP manager is available
	if s.deps.Router == nil {
		return connect.NewResponse(&reliantv1.ListServersResponse{
			Servers: []*reliantv1.MCPServer{},
			Total:   int32(len(scopedServers)),
		}), nil
	}

	names := make([]string, 0, len(scopedServers))
	anyEnabled := false
	for name, scoped := range scopedServers {
		names = append(names, name)
		anyEnabled = anyEnabled || scoped.Config.IsEnabled()
	}
	sort.Strings(names)

	// Only an enabled server has a runtime status to report.
	var status *daemonMCPServerStatus
	if anyEnabled {
		status = s.serverStatus(ctx, userID, checkout)
	}

	// Build server list from merged scoped config (source of truth)
	servers := make([]*reliantv1.MCPServer, 0, len(scopedServers))
	for _, name := range names {
		scoped := scopedServers[name]
		server := mcpServerFromConfig(name, scoped)
		if !scoped.Config.IsEnabled() {
			server.Status = reliantv1.MCPServerStatus_MCP_SERVER_STATUS_DISABLED
		} else {
			applyServerStatus(server, status)
		}
		servers = append(servers, server)
	}

	return connect.NewResponse(&reliantv1.ListServersResponse{
		Servers: servers,
		Total:   int32(len(servers)),
	}), nil
}
