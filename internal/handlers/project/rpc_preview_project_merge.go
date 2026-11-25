package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// PreviewProjectMerge implements the PreviewProjectMerge RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=project.PreviewProjectMerge
func (s *Service) PreviewProjectMerge(
	ctx context.Context,
	req *connect.Request[pb.PreviewProjectMergeRequest],
) (*connect.Response[pb.PreviewProjectMergeResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("PreviewProjectMerge"))
}
