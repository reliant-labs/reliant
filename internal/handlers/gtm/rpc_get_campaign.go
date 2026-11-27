package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetCampaign returns a campaign with its ideas and items (capped) and its
// coverage over all its items.
func (s *Service) GetCampaign(
	ctx context.Context,
	req *connect.Request[pb.GetCampaignRequest],
) (*connect.Response[pb.GetCampaignResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	d, err := s.deps.Service.GetCampaign(ctx, a, gtmsvc.GetCampaignInput{ID: m.GetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetCampaignResponse{
		Campaign:  campaignOut(d.Campaign),
		Ideas:     ideasOut(d.Ideas),
		Items:     itemsOut(d.Items),
		Truncated: d.Truncated,
		Coverage:  coverageOut(d.Coverage),
	}), nil
}
