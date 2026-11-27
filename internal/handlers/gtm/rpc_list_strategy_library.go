package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListStrategyLibrary implements the ListStrategyLibrary RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListStrategyLibrary
func (s *Service) ListStrategyLibrary(
	ctx context.Context,
	req *connect.Request[pb.ListStrategyLibraryRequest],
) (*connect.Response[pb.ListStrategyLibraryResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListStrategyLibrary"))
}
