package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpdateItem edits an item, its schedule or its status along the lifecycle;
// unset fields are left alone, clear_scheduled_at wins over scheduled_at, and
// a set recipient list replaces the stored one.
func (s *Service) UpdateItem(
	ctx context.Context,
	req *connect.Request[pb.UpdateItemRequest],
) (*connect.Response[pb.UpdateItemResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	scheduled, err := timeIn("scheduled_at", m.GetScheduledAt())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	in := gtmsvc.UpdateItemInput{
		ID:               m.GetId(),
		ExpectedRevision: m.GetExpectedRevision(),
		Format:           optEnumIn[gtmsvc.ItemFormat](m.Format),
		Title:            m.Title,
		BodyMD:           m.BodyMd,
		ChannelID:        m.ChannelId,
		CampaignID:       m.CampaignId,
		IdeaID:           m.IdeaId,
		ScheduledAt:      scheduled,
		ClearScheduledAt: m.GetClearScheduledAt(),
		EmailSubject:     m.EmailSubject,
		Status:           optEnumIn[gtmsvc.ItemStatus](m.Status),
		PublishedURL:     m.PublishedUrl,
	}
	if m.GetEmailTo() != nil {
		to := append([]string{}, m.GetEmailTo().GetAddresses()...)
		in.EmailTo = &to
	}
	item, err := s.deps.Service.UpdateItem(ctx, a, in)
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpdateItemResponse{Item: itemOut(item)}), nil
}
