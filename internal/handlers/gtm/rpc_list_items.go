package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListItems lists the org's items; a scheduled range makes it the calendar.
func (s *Service) ListItems(
	ctx context.Context,
	req *connect.Request[pb.ListItemsRequest],
) (*connect.Response[pb.ListItemsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	from, err := timeIn("scheduled_from", m.GetScheduledFrom())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	to, err := timeIn("scheduled_to", m.GetScheduledTo())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	res, err := s.deps.Service.ListItems(ctx, a, gtmsvc.ListItemsInput{
		CampaignID:    m.CampaignId,
		Format:        enumIn[gtmsvc.ItemFormat](m.GetFormat()),
		ChannelID:     m.ChannelId,
		Status:        enumIn[gtmsvc.ItemStatus](m.GetStatus()),
		ScheduledFrom: from,
		ScheduledTo:   to,
		PageSize:      int(m.GetPageSize()),
		PageToken:     m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListItemsResponse{
		Items:         itemsOut(res.Items),
		NextPageToken: res.NextPageToken,
	}), nil
}
