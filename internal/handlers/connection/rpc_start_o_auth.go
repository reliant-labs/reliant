package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) StartOAuth(ctx context.Context, req *connect.Request[reliantv1.StartOAuthRequest]) (*connect.Response[reliantv1.StartOAuthResponse], error) {
	caller := connectionrpc.Caller
	if req.Msg.GetOwnerOrgId() != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	url, err := s.deps.Connections.StartOAuth(ctx, connections.StartParams{
		UserID: userID, IntegrationID: req.Msg.GetIntegrationId(), Name: req.Msg.GetName(),
		ReconnectID: req.Msg.GetReconnectId(), RedirectAfter: req.Msg.GetRedirectAfter(),
		Params: req.Msg.GetParams(),
		// The browser sets Origin; page script cannot. It names the web app
		// the provider's redirect is relayed back to.
		ClientOrigin:     req.Header().Get("Origin"),
		LoopbackRedirect: req.Msg.GetLoopbackRedirect(),
		OrgID:            req.Msg.GetOwnerOrgId(),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.StartOAuthResponse{AuthorizeUrl: url}), nil
}
