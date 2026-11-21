package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func (s *Service) RenameConnection(ctx context.Context, req *connect.Request[reliantv1.RenameConnectionRequest]) (*connect.Response[reliantv1.RenameConnectionResponse], error) {
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
		c, err = s.deps.Connections.RenameOrg(ctx, userID, org, req.Msg.GetId(), req.Msg.GetName())
	} else {
		c, err = s.deps.Connections.Rename(ctx, userID, req.Msg.GetId(), req.Msg.GetName())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.RenameConnectionResponse{Connection: connectionToProto(c)}), nil
}
