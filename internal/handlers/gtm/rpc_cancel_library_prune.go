package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CancelLibraryPrune implements the CancelLibraryPrune RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.CancelLibraryPrune
func (s *Service) CancelLibraryPrune(
	ctx context.Context,
	req *connect.Request[pb.CancelLibraryPruneRequest],
) (*connect.Response[pb.CancelLibraryPruneResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("CancelLibraryPrune"))
}
