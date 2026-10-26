package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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
		status = s.serverStatus(ctx, userID, projectPath)
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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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
	status := s.serverStatus(ctx, userID, projectPath)
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
	projectPath, err := s.getProjectPath(ctx, projectID, userID)
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
	if _, ok := s.serverStatus(ctx, userID, projectPath).entry(name); !ok {
		return connect.NewResponse(&reliantv1.GetServerToolsResponse{Tools: []*reliantv1.MCPTool{}, Total: 0}), nil
	}

	// The tools themselves are a live question for the server; ask within
	// the request's own budget.
	tools, err := newDaemonMCPProxy(s.deps.Router, userID).listTools(ctx, projectPath, name)
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

// ListRecommended returns a list of recommended MCP servers
func (s *Service) ListRecommended(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRecommendedRequest],
) (*connect.Response[reliantv1.ListRecommendedResponse], error) {
	projectID := req.Msg.ProjectId
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	userID := auth.MustGetUserID(ctx)
	if err := s.projectBelongsToUser(ctx, projectID, userID); err != nil {
		return nil, err
	}

	// Load recommended MCPs from YAML config
	recommendedConfig, err := config.LoadRecommendedMCPs()
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load recommended MCPs"))
	}

	// Get currently installed servers from merged scoped configs (source of truth)
	installedServers := make(map[string]bool)
	scopedServers, err := s.readMergedScopedServers(ctx, projectID)
	if err == nil {
		for name := range scopedServers {
			installedServers[name] = true
		}
	}

	// Build response from YAML config
	recommended := make([]*reliantv1.RecommendedServer, 0, len(recommendedConfig.Recommended))
	for _, rec := range recommendedConfig.Recommended {
		// Convert config fields
		configFields := make([]*reliantv1.ConfigField, 0, len(rec.ConfigFields))
		for _, field := range rec.ConfigFields {
			cf := &reliantv1.ConfigField{
				Key:      field.Key,
				Label:    field.Label,
				Type:     field.Type,
				Required: field.Required,
			}
			if field.Placeholder != "" {
				cf.Placeholder = &field.Placeholder
			}
			if field.HelpText != "" {
				cf.HelpText = &field.HelpText
			}
			if field.ValidationRegex != "" {
				cf.ValidationRegex = &field.ValidationRegex
			}
			if field.ValidationMessage != "" {
				cf.ValidationMessage = &field.ValidationMessage
			}
			configFields = append(configFields, cf)
		}

		// Convert server config
		serverCfg := rec.Config.ToMCPServer()
		protoConfig := &reliantv1.MCPServerConfig{
			Command: serverCfg.Command,
			Args:    serverCfg.Args,
			Env:     serverCfg.Env,
			Headers: serverCfg.Headers,
			Type:    string(serverCfg.Type),
		}
		if serverCfg.URL != "" {
			protoConfig.Url = &serverCfg.URL
		}

		server := &reliantv1.RecommendedServer{
			Name:         rec.Name,
			DisplayName:  rec.DisplayName,
			Description:  rec.Description,
			Category:     rec.Category,
			ConfigFields: configFields,
			Config:       protoConfig,
			Installed:    installedServers[rec.Name],
		}
		if rec.Bundles != nil && len(rec.Bundles.Skills) > 0 {
			skillNames := make([]string, 0)
			for _, bundle := range rec.Bundles.Skills {
				if strings.TrimSpace(bundle.Name) != "" {
					skillNames = append(skillNames, bundle.Name)
				}
			}
			if len(skillNames) > 0 {
				msg := fmt.Sprintf("Includes %d bundled skill(s): %s", len(skillNames), strings.Join(skillNames, ", "))
				server.Description = strings.TrimSpace(server.Description + "\n\n" + msg)
			}
		}

		if rec.SetupRequired {
			server.SetupRequired = &rec.SetupRequired
		}
		if rec.SetupInstructions != "" {
			server.SetupInstructions = &rec.SetupInstructions
		}
		if rec.ConfigTemplate != "" {
			server.ConfigTemplate = &rec.ConfigTemplate
		}
		if rec.DocsURL != "" {
			server.DocsUrl = &rec.DocsURL
		}

		recommended = append(recommended, server)
	}

	return connect.NewResponse(&reliantv1.ListRecommendedResponse{
		Recommended: recommended,
		Total:       int32(len(recommended)),
	}), nil
}
