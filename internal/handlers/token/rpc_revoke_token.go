package token

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

// RevokeToken revokes one of the caller's tokens.
func (s *Service) RevokeToken(
	ctx context.Context, req *connect.Request[reliantv1.RevokeTokenRequest],
) (*connect.Response[reliantv1.RevokeTokenResponse], error) {
	userID, _, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, svcerr.InvalidArgument("token id is required")
	}
	if err := s.deps.Authority.RevokeForUser(ctx, userID, id); err != nil {
		return nil, authorityError("revoke", err)
	}
	logging.InfoContext(ctx, "access token revoked", "user_id", userID, "token_id", id)
	return connect.NewResponse(&reliantv1.RevokeTokenResponse{}), nil
}
