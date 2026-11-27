package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetIdea implements the GetIdea RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.GetIdea
func (s *Service) GetIdea(
	ctx context.Context,
	req *connect.Request[pb.GetIdeaRequest],
) (*connect.Response[pb.GetIdeaResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("GetIdea"))
}
