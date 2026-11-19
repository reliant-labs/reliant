package mcp

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/config"
)

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
