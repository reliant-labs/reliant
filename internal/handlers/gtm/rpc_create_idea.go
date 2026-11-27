package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CreateIdea implements the CreateIdea RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.CreateIdea
func (s *Service) CreateIdea(
	ctx context.Context,
	req *connect.Request[pb.CreateIdeaRequest],
) (*connect.Response[pb.CreateIdeaResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("CreateIdea"))
}
