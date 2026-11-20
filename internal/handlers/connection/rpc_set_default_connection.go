package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) SetDefaultConnection(ctx context.Context, req *reliantv1.SetDefaultConnectionRequest) (*reliantv1.SetDefaultConnectionResponse, error) {
	org := req.GetOwnerOrgId()
	caller := connectionrpc.Caller
	if org != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var c *core.Connection
	if org != "" {
		c, err = s.deps.Connections.SetDefaultOrg(ctx, userID, org, req.GetId())
	} else {
		c, err = s.deps.Connections.SetDefault(ctx, userID, req.GetId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.SetDefaultConnectionResponse{Connection: connectionToProto(c)}, nil
}
