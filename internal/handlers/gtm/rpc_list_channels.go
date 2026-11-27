package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListChannels lists the org's channels by key (the domain seeds the
// defaults on an org's first read).
func (s *Service) ListChannels(
	ctx context.Context,
	req *connect.Request[pb.ListChannelsRequest],
) (*connect.Response[pb.ListChannelsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListChannels(ctx, a, gtmsvc.ListChannelsInput{
		Status: enumIn[gtmsvc.ChannelStatus](m.GetStatus()),
		Mode:   enumIn[gtmsvc.ChannelMode](m.GetMode()),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListChannelsResponse{Channels: channelsOut(res.Channels)}), nil
}
