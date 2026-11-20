package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) CreateApiKeyConnection(ctx context.Context, req *connect.Request[reliantv1.CreateApiKeyConnectionRequest]) (*connect.Response[reliantv1.CreateApiKeyConnectionResponse], error) {
	caller := connectionrpc.Caller
	if req.Msg.GetOwnerOrgId() != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var kind connections.APIKeyKind
	switch req.Msg.GetKind() {
	case reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY:
		kind = connections.APIKeyKindAPIKey
	case reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_BASIC:
		kind = connections.APIKeyKindBasic
	}
	c, err := s.deps.Connections.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: req.Msg.GetIntegrationId(), Name: req.Msg.GetName(),
		Kind: kind, Fields: req.Msg.GetFields(), Params: req.Msg.GetParams(), OrgID: req.Msg.GetOwnerOrgId(),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.CreateApiKeyConnectionResponse{Connection: connectionToProto(c)}), nil
}
