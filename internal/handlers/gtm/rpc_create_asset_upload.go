package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// CreateAssetUpload implements the CreateAssetUpload RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.CreateAssetUpload
func (s *Service) CreateAssetUpload(
	ctx context.Context,
	req *connect.Request[pb.CreateAssetUploadRequest],
) (*connect.Response[pb.CreateAssetUploadResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("CreateAssetUpload"))
}
