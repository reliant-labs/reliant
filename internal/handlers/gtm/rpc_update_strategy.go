package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpdateStrategy edits a strategy or moves its status; unset fields are left
// alone, and a category of UNSPECIFIED clears it.
func (s *Service) UpdateStrategy(
	ctx context.Context,
	req *connect.Request[pb.UpdateStrategyRequest],
) (*connect.Response[pb.UpdateStrategyResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	st, err := s.deps.Service.UpdateStrategy(ctx, a, gtmsvc.UpdateStrategyInput{
		ID:               m.GetId(),
		ExpectedRevision: m.GetExpectedRevision(),
		Name:             m.Name,
		GoalMD:           m.GoalMd,
		TargetMetric:     m.TargetMetric,
		PeriodStart:      m.PeriodStart,
		PeriodEnd:        m.PeriodEnd,
		Status:           optEnumIn[gtmsvc.StrategyStatus](m.Status),
		Category:         optEnumIn[gtmsvc.StrategyCategory](m.Category),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpdateStrategyResponse{Strategy: strategyOut(st)}), nil
}
