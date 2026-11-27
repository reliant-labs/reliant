package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListChannels implements the ListChannels RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListChannels
func (s *Service) ListChannels(
	ctx context.Context,
	req *connect.Request[pb.ListChannelsRequest],
) (*connect.Response[pb.ListChannelsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListChannels"))
}
