package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UpdateIdea implements the UpdateIdea RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.UpdateIdea
func (s *Service) UpdateIdea(
	ctx context.Context,
	req *connect.Request[pb.UpdateIdeaRequest],
) (*connect.Response[pb.UpdateIdeaResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("UpdateIdea"))
}
