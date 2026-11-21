package daemon

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// CloseOAuthHelper closes the helper port when the linking session ends.
func (s *Service) CloseOAuthHelper(
	ctx context.Context,
	_ *connect.Request[reliantv1.CloseOAuthHelperRequest],
) (*connect.Response[reliantv1.CloseOAuthHelperResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}

	if s.deps.Router == nil {
		return nil, errNoRouter()
	}
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "auth.close_oauth_helper", []byte("{}"), openHelperTimeoutMs)
	if err != nil {
		return nil, oauthDaemonError("close OAuth helper", err)
	}

	var resp struct {
		Closed bool `json:"closed"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}

	return connect.NewResponse(&reliantv1.CloseOAuthHelperResponse{Closed: resp.Closed}), nil
}
