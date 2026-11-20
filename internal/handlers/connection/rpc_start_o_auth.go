package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) StartOAuth(ctx context.Context, req *reliantv1.StartOAuthRequest) (*reliantv1.StartOAuthResponse, error) {
	info := serverkit.CallInfoV2(ctx)
	caller := connectionrpc.Caller
	if req.GetOwnerOrgId() != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	url, err := s.deps.Connections.StartOAuth(ctx, connections.StartParams{
		UserID: userID, IntegrationID: req.GetIntegrationId(), Name: req.GetName(),
		ReconnectID: req.GetReconnectId(), RedirectAfter: req.GetRedirectAfter(),
		Params: req.GetParams(),
		// The browser sets Origin; page script cannot. It names the web app
		// the provider's redirect is relayed back to.
		ClientOrigin:     info.RequestHeader().Get("Origin"),
		LoopbackRedirect: req.GetLoopbackRedirect(),
		OrgID:            req.GetOwnerOrgId(),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.StartOAuthResponse{AuthorizeUrl: url}, nil
}
