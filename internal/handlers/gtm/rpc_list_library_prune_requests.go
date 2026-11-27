package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListLibraryPruneRequests implements the ListLibraryPruneRequests RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ListLibraryPruneRequests
func (s *Service) ListLibraryPruneRequests(
	ctx context.Context,
	req *connect.Request[pb.ListLibraryPruneRequestsRequest],
) (*connect.Response[pb.ListLibraryPruneRequestsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListLibraryPruneRequests"))
}
