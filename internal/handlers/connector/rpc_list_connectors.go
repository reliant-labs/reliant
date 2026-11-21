package connector

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListConnectors returns the caller's connectors.
func (s *Service) ListConnectors(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListConnectorsRequest],
) (*connect.Response[reliantv1.ListConnectorsResponse], error) {
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
	return connect.NewResponse(&reliantv1.ListConnectorsResponse{Connectors: out}), nil
}
