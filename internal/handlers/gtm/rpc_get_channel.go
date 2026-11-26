package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetChannel returns one channel of the org.
func (s *Service) GetChannel(
	ctx context.Context,
	req *connect.Request[pb.GetChannelRequest],
) (*connect.Response[pb.GetChannelResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Service.GetChannel(ctx, a, gtmsvc.GetChannelInput{ID: m.GetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetChannelResponse{Channel: channelOut(c)}), nil
}
