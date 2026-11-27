package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpdatePositioning replaces the org's positioning whole; expected revision 0
// creates it.
func (s *Service) UpdatePositioning(
	ctx context.Context,
	req *connect.Request[pb.UpdatePositioningRequest],
) (*connect.Response[pb.UpdatePositioningResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	p, err := s.deps.Service.UpdatePositioning(ctx, a, gtmsvc.UpdatePositioningInput{
		ExpectedRevision: m.GetExpectedRevision(),
		PositioningMD:    m.GetPositioningMd(),
		Audiences:        m.GetAudiences(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpdatePositioningResponse{Positioning: positioningOut(p)}), nil
}
