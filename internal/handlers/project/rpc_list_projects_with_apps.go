package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListProjectsWithApps implements the ListProjectsWithApps RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=project.ListProjectsWithApps
func (s *Service) ListProjectsWithApps(
	ctx context.Context,
	req *connect.Request[pb.ListProjectsWithAppsRequest],
) (*connect.Response[pb.ListProjectsWithAppsResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ListProjectsWithApps"))
}
