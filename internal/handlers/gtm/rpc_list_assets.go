package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListAssets lists the assets of an item or a campaign, newest first.
func (s *Service) ListAssets(
	ctx context.Context,
	req *connect.Request[pb.ListAssetsRequest],
) (*connect.Response[pb.ListAssetsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListAssets(ctx, a, gtmsvc.ListAssetsInput{
		ItemID:     m.GetItemId(),
		CampaignID: m.GetCampaignId(),
		PageSize:   int(m.GetPageSize()),
		PageToken:  m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListAssetsResponse{
		Assets:        assetsOut(res.Assets),
		NextPageToken: res.NextPageToken,
	}), nil
}
