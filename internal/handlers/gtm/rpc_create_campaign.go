package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CreateCampaign adds a campaign under a strategy of the org.
func (s *Service) CreateCampaign(
	ctx context.Context,
	req *connect.Request[pb.CreateCampaignRequest],
) (*connect.Response[pb.CreateCampaignResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	start, err := timeIn("period_start", m.GetPeriodStart())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	end, err := timeIn("period_end", m.GetPeriodEnd())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	c, err := s.deps.Service.CreateCampaign(ctx, a, gtmsvc.CreateCampaignInput{
		Name:              m.GetName(),
		Goal:              m.GetGoal(),
		PeriodStart:       start,
		PeriodEnd:         end,
		Status:            enumIn[gtmsvc.CampaignStatus](m.GetStatus()),
		StrategyID:        m.GetStrategyId(),
		PlannedChannelIDs: m.GetPlannedChannelIds(),
		PlannedFormats:    formatsIn(m.GetPlannedFormats()),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CreateCampaignResponse{Campaign: campaignOut(c)}), nil
}
