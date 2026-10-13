package tools_daemon

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ConnectGateway was never implemented server-side: the gateway dials a
// daemon's ConnectGateway as a client (RegisterOutboundConnection), it does
// not serve it. It answers exactly as before.
func (s *Service) ConnectGateway(ctx context.Context, stream *connect.BidiStream[pb.ServerMessage, pb.DaemonMessage]) error {
	return s.UnimplementedToolsDaemonServiceHandler.ConnectGateway(ctx, stream)
}
