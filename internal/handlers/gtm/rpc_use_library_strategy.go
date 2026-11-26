package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UseLibraryStrategy makes a DRAFT strategy from a library entry, and
// optionally a first DRAFT campaign under it.
func (s *Service) UseLibraryStrategy(
	ctx context.Context,
	req *connect.Request[pb.UseLibraryStrategyRequest],
) (*connect.Response[pb.UseLibraryStrategyResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.UseLibraryStrategy(ctx, a, gtmsvc.UseLibraryStrategyInput{
		LibraryKey:          m.GetLibraryKey(),
		CreateFirstCampaign: m.GetCreateFirstCampaign(),
		CampaignName:        m.CampaignName,
	})
	if err != nil {
		return nil, wireErr(err)
	}
	resp := &pb.UseLibraryStrategyResponse{Strategy: strategyOut(res.Strategy)}
	if res.Campaign != nil {
		resp.Campaign = campaignOut(*res.Campaign)
	}
	return connect.NewResponse(resp), nil
}
