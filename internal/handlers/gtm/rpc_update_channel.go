package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpdateChannel edits a channel; unset fields are left alone, and a set
// channel_types list (possibly empty) replaces the stored one.
func (s *Service) UpdateChannel(
	ctx context.Context,
	req *connect.Request[pb.UpdateChannelRequest],
) (*connect.Response[pb.UpdateChannelResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	in := gtmsvc.UpdateChannelInput{
		ID:                   m.GetId(),
		ExpectedRevision:     m.GetExpectedRevision(),
		Key:                  m.Key,
		Name:                 m.Name,
		Mode:                 optEnumIn[gtmsvc.ChannelMode](m.Mode),
		IntegrationKind:      m.IntegrationKind,
		ConnectionID:         m.ConnectionId,
		ManualInstructionsMD: m.ManualInstructionsMd,
		Status:               optEnumIn[gtmsvc.ChannelStatus](m.Status),
	}
	if m.GetChannelTypes() != nil {
		types := append([]string{}, m.GetChannelTypes().GetTypes()...)
		in.ChannelTypes = &types
	}
	c, err := s.deps.Service.UpdateChannel(ctx, a, in)
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpdateChannelResponse{Channel: channelOut(c)}), nil
}
