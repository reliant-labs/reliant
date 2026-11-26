package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetStrategy implements the GetStrategy RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.GetStrategy
func (s *Service) GetStrategy(
	ctx context.Context,
	req *connect.Request[pb.GetStrategyRequest],
) (*connect.Response[pb.GetStrategyResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetStrategy"))
}
