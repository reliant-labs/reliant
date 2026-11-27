package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpdateCampaign edits a campaign; unset fields are left alone, a clear flag
// wins over a value, and a set planned list (possibly empty) replaces the
// stored one.
func (s *Service) UpdateCampaign(
	ctx context.Context,
	req *connect.Request[pb.UpdateCampaignRequest],
) (*connect.Response[pb.UpdateCampaignResponse], error) {
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
	in := gtmsvc.UpdateCampaignInput{
		ID:               m.GetId(),
		ExpectedRevision: m.GetExpectedRevision(),
		Name:             m.Name,
		Goal:             m.Goal,
		PeriodStart:      start,
		PeriodEnd:        end,
		ClearPeriodStart: m.GetClearPeriodStart(),
		ClearPeriodEnd:   m.GetClearPeriodEnd(),
		Status:           optEnumIn[gtmsvc.CampaignStatus](m.Status),
		StrategyID:       m.StrategyId,
	}
	if m.GetPlannedChannelIds() != nil {
		ids := append([]string{}, m.GetPlannedChannelIds().GetIds()...)
		in.PlannedChannelIDs = &ids
	}
	if m.GetPlannedFormats() != nil {
		formats := append([]gtmsvc.ItemFormat{}, formatsIn(m.GetPlannedFormats().GetFormats())...)
		in.PlannedFormats = &formats
	}
	c, err := s.deps.Service.UpdateCampaign(ctx, a, in)
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpdateCampaignResponse{Campaign: campaignOut(c)}), nil
}
