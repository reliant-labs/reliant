package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListLibraryImports implements the ListLibraryImports RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListLibraryImports
func (s *Service) ListLibraryImports(
	ctx context.Context,
	req *connect.Request[pb.ListLibraryImportsRequest],
) (*connect.Response[pb.ListLibraryImportsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListLibraryImports"))
}
