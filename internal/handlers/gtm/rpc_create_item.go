package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CreateItem adds a DRAFT or REVIEW item.
func (s *Service) CreateItem(
	ctx context.Context,
	req *connect.Request[pb.CreateItemRequest],
) (*connect.Response[pb.CreateItemResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	scheduled, err := timeIn("scheduled_at", m.GetScheduledAt())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	item, err := s.deps.Service.CreateItem(ctx, a, gtmsvc.CreateItemInput{
		Status:       enumIn[gtmsvc.ItemStatus](m.GetStatus()),
		Format:       enumIn[gtmsvc.ItemFormat](m.GetFormat()),
		Title:        m.GetTitle(),
		BodyMD:       m.GetBodyMd(),
		ChannelID:    m.GetChannelId(),
		CampaignID:   m.GetCampaignId(),
		IdeaID:       m.GetIdeaId(),
		ScheduledAt:  scheduled,
		EmailTo:      m.GetEmailTo(),
		EmailSubject: m.GetEmailSubject(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CreateItemResponse{Item: itemOut(item)}), nil
}
