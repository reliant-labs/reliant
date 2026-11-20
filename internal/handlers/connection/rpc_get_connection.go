package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) GetConnection(ctx context.Context, req *reliantv1.GetConnectionRequest) (*reliantv1.GetConnectionResponse, error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	var c *core.Connection
	if org := req.GetOwnerOrgId(); org != "" {
		c, err = s.deps.Connections.GetOrg(ctx, userID, org, req.GetId())
	} else {
		c, err = s.deps.Connections.Get(ctx, userID, req.GetId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.GetConnectionResponse{Connection: connectionToProto(c)}, nil
}
