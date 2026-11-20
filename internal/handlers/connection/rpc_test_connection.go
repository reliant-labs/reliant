package connection

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) TestConnection(ctx context.Context, req *connect.Request[reliantv1.TestConnectionRequest]) (*connect.Response[reliantv1.TestConnectionResponse], error) {
	var r *connections.TestResult
	if org := req.Msg.GetOwnerOrgId(); org != "" {
		userID, err := orgAdminCaller(ctx)
		if err != nil {
			return nil, err
		}
		r, err = s.deps.Connections.TestOrg(ctx, userID, org, req.Msg.GetId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
		}
		return connect.NewResponse(&reliantv1.TestConnectionResponse{
			Ok: r.OK, Probed: r.Probed, AccountLabel: r.AccountLabel, ErrorClass: r.ErrorClass,
		}), nil
	}
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	r, err = s.deps.Connections.Test(ctx, userID, req.Msg.GetId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return connect.NewResponse(&reliantv1.TestConnectionResponse{
		Ok: r.OK, Probed: r.Probed, AccountLabel: r.AccountLabel, ErrorClass: r.ErrorClass,
	}), nil
}
