package connector

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// ListAuthorizedClients returns the applications acting through the caller's
// connectors.
func (s *Service) ListAuthorizedClients(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListAuthorizedClientsRequest],
) (*connect.Response[reliantv1.ListAuthorizedClientsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	bindings, err := s.deps.Grants.ListBindingsByUser(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list authorized clients"))
	}

	// Connector names are resolved so the UI can say "ChatGPT → my-project"
	// rather than showing an opaque id pair.
	names := map[string]string{}
	if grants, err := s.deps.Grants.ListGrantsByUser(ctx, userID); err == nil {
		for _, g := range grants {
			names[g.ID] = g.Name
		}
	}

	out := make([]*reliantv1.AuthorizedClient, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, &reliantv1.AuthorizedClient{
			ClientId:      b.ClientID,
			ClientName:    b.ClientName,
			ConnectorId:   b.GrantID,
			ConnectorName: names[b.GrantID],
			AuthorizedAt:  b.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListAuthorizedClientsResponse{Clients: out}), nil
}
