package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetStrategy returns one strategy with its campaigns (capped; truncated says
// when they were cut).
func (s *Service) GetStrategy(
	ctx context.Context,
	req *connect.Request[pb.GetStrategyRequest],
) (*connect.Response[pb.GetStrategyResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	d, err := s.deps.Service.GetStrategy(ctx, a, gtmsvc.GetStrategyInput{ID: m.GetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetStrategyResponse{
		Strategy:  strategyOut(d.Strategy),
		Campaigns: campaignsOut(d.Campaigns),
		Truncated: d.Truncated,
	}), nil
}
