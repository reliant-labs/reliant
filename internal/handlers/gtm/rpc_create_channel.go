package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CreateChannel implements the CreateChannel RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.CreateChannel
func (s *Service) CreateChannel(
	ctx context.Context,
	req *connect.Request[pb.CreateChannelRequest],
) (*connect.Response[pb.CreateChannelResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("CreateChannel"))
}
