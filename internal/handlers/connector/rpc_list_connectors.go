package connector

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListConnectors returns the caller's connectors.
func (s *Service) ListConnectors(
	ctx context.Context,
	_ *reliantv1.ListConnectorsRequest,
) (*reliantv1.ListConnectorsResponse, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	grants, err := s.deps.Grants.ListGrantsByUser(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list connectors"))
	}

	out := make([]*reliantv1.Connector, 0, len(grants))
	for _, g := range grants {
		out = append(out, toProtoConnector(g))
	}
	return &reliantv1.ListConnectorsResponse{Connectors: out}, nil
}
