package connector

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/mcpserver"
)

// ListAvailableTools returns the grantable catalog, so the UI never hardcodes
// a list that drifts from the server's.
func (s *Service) ListAvailableTools(
	ctx context.Context,
	req *reliantv1.ListAvailableToolsRequest,
) (*reliantv1.ListAvailableToolsResponse, error) {
	if _, ok := auth.GetUserIDFromContext(ctx); !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	if req.GetKind() == reliantv1.ConnectorKind_CONNECTOR_KIND_FORGE {
		forge := mcpserver.ForgeTools()
		tools := make([]*reliantv1.ConnectorTool, 0, len(forge))
		for _, t := range forge {
			tools = append(tools, &reliantv1.ConnectorTool{Name: t.Name, Description: t.Description, Mutating: t.Mutating})
		}
		return &reliantv1.ListAvailableToolsResponse{Tools: tools}, nil
	}

	catalog := mcpserver.Tools()
	tools := make([]*reliantv1.ConnectorTool, 0, len(catalog))
	for _, t := range catalog {
		tools = append(tools, &reliantv1.ConnectorTool{
			Name:        t.Name,
			Description: t.Description,
			Mutating:    t.Mutating,
			NeedsExec:   t.NeedsExec,
		})
	}
	return &reliantv1.ListAvailableToolsResponse{Tools: tools}, nil
}
