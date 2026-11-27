package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetStrategyLibraryEntry implements the GetStrategyLibraryEntry RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.GetStrategyLibraryEntry
func (s *Service) GetStrategyLibraryEntry(
	ctx context.Context,
	req *connect.Request[pb.GetStrategyLibraryEntryRequest],
) (*connect.Response[pb.GetStrategyLibraryEntryResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetStrategyLibraryEntry"))
}
