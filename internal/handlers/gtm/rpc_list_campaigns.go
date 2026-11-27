package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListCampaigns lists the org's campaigns, newest first.
func (s *Service) ListCampaigns(
	ctx context.Context,
	req *connect.Request[pb.ListCampaignsRequest],
) (*connect.Response[pb.ListCampaignsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListCampaigns(ctx, a, gtmsvc.ListCampaignsInput{
		Status:     enumIn[gtmsvc.CampaignStatus](m.GetStatus()),
		StrategyID: m.StrategyId,
		PageSize:   int(m.GetPageSize()),
		PageToken:  m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListCampaignsResponse{
		Campaigns:     campaignsOut(res.Campaigns),
		NextPageToken: res.NextPageToken,
	}), nil
}
