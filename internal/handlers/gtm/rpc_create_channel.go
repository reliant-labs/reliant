package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CreateChannel adds a channel to the org.
func (s *Service) CreateChannel(
	ctx context.Context,
	req *connect.Request[pb.CreateChannelRequest],
) (*connect.Response[pb.CreateChannelResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Service.CreateChannel(ctx, a, gtmsvc.CreateChannelInput{
		Key:                  m.GetKey(),
		Name:                 m.GetName(),
		Mode:                 enumIn[gtmsvc.ChannelMode](m.GetMode()),
		IntegrationKind:      m.GetIntegrationKind(),
		ConnectionID:         m.GetConnectionId(),
		ManualInstructionsMD: m.GetManualInstructionsMd(),
		ChannelTypes:         m.GetChannelTypes(),
		Status:               enumIn[gtmsvc.ChannelStatus](m.GetStatus()),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CreateChannelResponse{Channel: channelOut(c)}), nil
}
