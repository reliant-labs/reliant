package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CreateStrategy implements the CreateStrategy RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.CreateStrategy
func (s *Service) CreateStrategy(
	ctx context.Context,
	req *connect.Request[pb.CreateStrategyRequest],
) (*connect.Response[pb.CreateStrategyResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("CreateStrategy"))
}
