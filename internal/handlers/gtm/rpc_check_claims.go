package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CheckClaims checks a text against the org's claims registry; it stores
// nothing.
func (s *Service) CheckClaims(
	ctx context.Context,
	req *connect.Request[pb.CheckClaimsRequest],
) (*connect.Response[pb.CheckClaimsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.CheckClaims(ctx, a, gtmsvc.CheckClaimsInput{Text: m.GetText()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CheckClaimsResponse{
		Findings: findingsOut(res.Findings),
		Blocking: res.Blocking,
	}), nil
}
