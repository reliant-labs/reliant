package connector

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// RevokeConnector revokes one of the caller's connectors.
func (s *Service) RevokeConnector(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeConnectorRequest],
) (*connect.Response[reliantv1.RevokeConnectorResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, svcerr.InvalidArgument("connector id is required")
	}

	// Scoped by user in the store, so a guessed id cannot revoke someone
	// else's connector.
	revoked, err := s.deps.Grants.RevokeGrant(ctx, userID, id)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to revoke connector"))
	}
	// Kill the credential too, not only the policy: the grant is re-checked
	// per call, but a revoked grant must leave no live token behind.
	if revoked {
		if _, err := s.deps.Tokens.RevokeResource(ctx, connectorResource(id)); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Unavailable("failed to revoke connector credential"))
		}
	} else {
		activity.Skip(ctx) // already revoked: nothing changed
	}
	return connect.NewResponse(&reliantv1.RevokeConnectorResponse{Revoked: revoked}), nil
}
