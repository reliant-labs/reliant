package tools_daemon

import (
	"context"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// ConnectGateway was never implemented server-side: the gateway dials a
// daemon's ConnectGateway as a client (RegisterOutboundConnection), it does
// not serve it. It answers exactly as before.
func (s *Service) ConnectGateway(ctx context.Context, stream reliantv1connect.ToolsDaemonServiceConnectGatewayServerStream) error {
	return s.UnimplementedToolsDaemonServiceHandler.ConnectGateway(ctx, stream)
}
