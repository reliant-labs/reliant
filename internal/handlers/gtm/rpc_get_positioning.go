package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetPositioning returns the org's positioning; revision 0 when it has none.
func (s *Service) GetPositioning(
	ctx context.Context,
	req *connect.Request[pb.GetPositioningRequest],
) (*connect.Response[pb.GetPositioningResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	p, err := s.deps.Service.GetPositioning(ctx, a)
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetPositioningResponse{Positioning: positioningOut(p)}), nil
}
