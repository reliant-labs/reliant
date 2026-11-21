package daemon

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// OpenOAuthHelper asks the user's daemon to open its localhost OAuth helper
// port for one linking session.
func (s *Service) OpenOAuthHelper(
	ctx context.Context,
	req *connect.Request[reliantv1.OpenOAuthHelperRequest],
) (*connect.Response[reliantv1.OpenOAuthHelperResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}

	payload, err := json.Marshal(map[string]any{
		"web_origin": req.Msg.WebOrigin,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	if s.deps.Router == nil {
		return nil, errNoRouter()
	}
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "auth.open_oauth_helper", payload, openHelperTimeoutMs)
	if err != nil {
		return nil, oauthDaemonError("open OAuth helper", err)
	}

	var resp struct {
		Port           int32  `json:"port"`
		Addr           string `json:"addr"`
		AlreadyRunning bool   `json:"already_running"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}

	return connect.NewResponse(&reliantv1.OpenOAuthHelperResponse{
		Port:           resp.Port,
		Addr:           resp.Addr,
		AlreadyRunning: resp.AlreadyRunning,
	}), nil
}
