package connection

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
)

func (s *Service) TestConnection(ctx context.Context, req *reliantv1.TestConnectionRequest) (*reliantv1.TestConnectionResponse, error) {
	var r *connections.TestResult
	if org := req.GetOwnerOrgId(); org != "" {
		userID, err := orgAdminCaller(ctx)
		if err != nil {
			return nil, err
		}
		r, err = s.deps.Connections.TestOrg(ctx, userID, org, req.GetId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
		}
		return &reliantv1.TestConnectionResponse{
			Ok: r.OK, Probed: r.Probed, AccountLabel: r.AccountLabel, ErrorClass: r.ErrorClass,
		}, nil
	}
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	r, err = s.deps.Connections.Test(ctx, userID, req.GetId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("connection service error"))
	}
	return &reliantv1.TestConnectionResponse{
		Ok: r.OK, Probed: r.Probed, AccountLabel: r.AccountLabel, ErrorClass: r.ErrorClass,
	}, nil
}
