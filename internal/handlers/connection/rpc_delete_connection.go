package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
)

func (s *Service) DeleteConnection(ctx context.Context, req *connect.Request[reliantv1.DeleteConnectionRequest]) (*connect.Response[reliantv1.DeleteConnectionResponse], error) {
	org := req.Msg.GetOwnerOrgId()
	caller := connectionrpc.Caller
	if org != "" {
		caller = orgAdminCaller
	}
	userID, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if org != "" {
		err = s.deps.Connections.DeleteOrg(ctx, userID, org, req.Msg.GetId())
	} else {
		err = s.deps.Connections.Delete(ctx, userID, req.Msg.GetId())
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.DeleteConnectionResponse{}), nil
}
