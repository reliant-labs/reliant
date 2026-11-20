package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) CreateApiKeyConnection(ctx context.Context, req *reliantv1.CreateApiKeyConnectionRequest) (*reliantv1.CreateApiKeyConnectionResponse, error) {
	caller := connectionrpc.Caller
	if req.GetOwnerOrgId() != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var kind connections.APIKeyKind
	switch req.GetKind() {
	case reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY:
		kind = connections.APIKeyKindAPIKey
	case reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_BASIC:
		kind = connections.APIKeyKindBasic
	}
	c, err := s.deps.Connections.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: req.GetIntegrationId(), Name: req.GetName(),
		Kind: kind, Fields: req.GetFields(), Params: req.GetParams(), OrgID: req.GetOwnerOrgId(),
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.CreateApiKeyConnectionResponse{Connection: connectionToProto(c)}, nil
}
