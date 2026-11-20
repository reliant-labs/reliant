package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) RenameConnection(ctx context.Context, req *reliantv1.RenameConnectionRequest) (*reliantv1.RenameConnectionResponse, error) {
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
		c, err = s.deps.Connections.RenameOrg(ctx, userID, org, req.GetId(), req.GetName())
	} else {
		c, err = s.deps.Connections.Rename(ctx, userID, req.GetId(), req.GetName())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.RenameConnectionResponse{Connection: connectionToProto(c)}, nil
}
