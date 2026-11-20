package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
)

func (s *Service) DeleteConnection(ctx context.Context, req *reliantv1.DeleteConnectionRequest) (*reliantv1.DeleteConnectionResponse, error) {
	org := req.GetOwnerOrgId()
	caller := connectionrpc.Caller
	if org != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if org != "" {
		err = s.deps.Connections.DeleteOrg(ctx, userID, org, req.GetId())
	} else {
		err = s.deps.Connections.Delete(ctx, userID, req.GetId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.DeleteConnectionResponse{}, nil
}
