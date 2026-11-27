package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CreateStrategy adds a strategy (DRAFT unless a person says otherwise).
func (s *Service) CreateStrategy(
	ctx context.Context,
	req *connect.Request[pb.CreateStrategyRequest],
) (*connect.Response[pb.CreateStrategyResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	st, err := s.deps.Service.CreateStrategy(ctx, a, gtmsvc.CreateStrategyInput{
		Name:         m.GetName(),
		GoalMD:       m.GetGoalMd(),
		TargetMetric: m.GetTargetMetric(),
		PeriodStart:  m.GetPeriodStart(),
		PeriodEnd:    m.GetPeriodEnd(),
		Status:       enumIn[gtmsvc.StrategyStatus](m.GetStatus()),
		Category:     enumIn[gtmsvc.StrategyCategory](m.GetCategory()),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CreateStrategyResponse{Strategy: strategyOut(st)}), nil
}
