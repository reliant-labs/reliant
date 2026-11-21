package daemon

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// StartOAuthFlow proxies the OAuth flow to the daemon which starts a localhost
// callback server and opens the browser.
func (s *Service) StartOAuthFlow(
	ctx context.Context,
	req *connect.Request[reliantv1.StartOAuthFlowRequest],
) (*connect.Response[reliantv1.StartOAuthFlowResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}

	payload, err := json.Marshal(map[string]any{
		"authorize_url_template": req.Msg.AuthorizeUrlTemplate,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	if s.deps.Router == nil {
		return nil, errNoRouter()
	}
	// Use a generous NATS-level timeout (1 hour). OAuth flows have no
	// application-level timeout — the frontend AbortController handles
	// cancellation, which propagates via ctx.
	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "auth.start_oauth", payload, 3_600_000)
	if err != nil {
		return nil, oauthDaemonError("OAuth flow failed", err)
	}

	var resp struct {
		Code        string `json:"code"`
		State       string `json:"state"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}

	return connect.NewResponse(&reliantv1.StartOAuthFlowResponse{
		Code:        resp.Code,
		State:       resp.State,
		RedirectUri: resp.RedirectURI,
	}), nil
}
