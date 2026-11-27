package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListStrategies lists the org's strategies, newest first.
func (s *Service) ListStrategies(
	ctx context.Context,
	req *connect.Request[pb.ListStrategiesRequest],
) (*connect.Response[pb.ListStrategiesResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListStrategies(ctx, a, gtmsvc.ListStrategiesInput{
		Status:    enumIn[gtmsvc.StrategyStatus](m.GetStatus()),
		PageSize:  int(m.GetPageSize()),
		PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListStrategiesResponse{
		Strategies:    strategiesOut(res.Strategies),
		NextPageToken: res.NextPageToken,
	}), nil
}
