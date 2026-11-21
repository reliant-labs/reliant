package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) SetDefaultConnection(ctx context.Context, req *connect.Request[reliantv1.SetDefaultConnectionRequest]) (*connect.Response[reliantv1.SetDefaultConnectionResponse], error) {
	org := req.Msg.GetOwnerOrgId()
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
		c, err = s.deps.Connections.SetDefaultOrg(ctx, userID, org, req.Msg.GetId())
	} else {
		c, err = s.deps.Connections.SetDefault(ctx, userID, req.Msg.GetId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.SetDefaultConnectionResponse{Connection: connectionToProto(c)}), nil
}
