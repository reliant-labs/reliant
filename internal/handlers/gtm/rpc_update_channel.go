package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UpdateChannel implements the UpdateChannel RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.UpdateChannel
func (s *Service) UpdateChannel(
	ctx context.Context,
	req *connect.Request[pb.UpdateChannelRequest],
) (*connect.Response[pb.UpdateChannelResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("UpdateChannel"))
}
