package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListClaims lists the org's claims registry.
func (s *Service) ListClaims(
	ctx context.Context,
	req *connect.Request[pb.ListClaimsRequest],
) (*connect.Response[pb.ListClaimsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListClaims(ctx, a, gtmsvc.ListClaimsInput{
		Status:    enumIn[gtmsvc.ClaimStatus](m.GetStatus()),
		Category:  enumIn[gtmsvc.ClaimCategory](m.GetCategory()),
		Search:    m.GetSearch(),
		PageSize:  int(m.GetPageSize()),
		PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListClaimsResponse{
		Claims:        claimsOut(res.Claims),
		NextPageToken: res.NextPageToken,
	}), nil
}
