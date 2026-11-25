package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// MergeProjects implements the MergeProjects RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=project.MergeProjects
func (s *Service) MergeProjects(
	ctx context.Context,
	req *connect.Request[pb.MergeProjectsRequest],
) (*connect.Response[pb.MergeProjectsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("MergeProjects"))
}
