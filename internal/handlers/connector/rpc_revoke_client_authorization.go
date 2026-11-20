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

// RevokeClientAuthorization disconnects one application.
//
// The connector survives: it may serve several applications, and disconnecting
// one should not cut off the others. Revoking the connector itself is the
// separate, broader action.
func (s *Service) RevokeClientAuthorization(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeClientAuthorizationRequest],
) (*connect.Response[reliantv1.RevokeClientAuthorizationResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	clientID := strings.TrimSpace(req.Msg.GetClientId())
	if clientID == "" {
		return nil, svcerr.InvalidArgument("client id is required")
	}

	revoked, err := s.deps.Grants.DeleteBinding(ctx, userID, clientID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to revoke authorization"))
	}
	if !revoked {
		activity.Skip(ctx) // no such authorization: nothing changed
	}
	return connect.NewResponse(&reliantv1.RevokeClientAuthorizationResponse{Revoked: revoked}), nil
}
