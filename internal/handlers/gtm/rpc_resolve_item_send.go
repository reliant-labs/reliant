package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ResolveItemSend implements the ResolveItemSend RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ResolveItemSend
func (s *Service) ResolveItemSend(
	ctx context.Context,
	req *connect.Request[pb.ResolveItemSendRequest],
) (*connect.Response[pb.ResolveItemSendResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ResolveItemSend"))
}
