package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) GetConnection(ctx context.Context, req *connect.Request[reliantv1.GetConnectionRequest]) (*connect.Response[reliantv1.GetConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	var c *core.Connection
	if org := req.Msg.GetOwnerOrgId(); org != "" {
		c, err = s.deps.Connections.GetOrg(ctx, userID, org, req.Msg.GetId())
	} else {
		c, err = s.deps.Connections.Get(ctx, userID, req.Msg.GetId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.GetConnectionResponse{Connection: connectionToProto(c)}), nil
}
