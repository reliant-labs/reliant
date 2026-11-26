package gtm

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ConfirmAssetUpload implements the ConfirmAssetUpload RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=gtm.ConfirmAssetUpload
func (s *Service) ConfirmAssetUpload(
	ctx context.Context,
	req *connect.Request[pb.ConfirmAssetUploadRequest],
) (*connect.Response[pb.ConfirmAssetUploadResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("ConfirmAssetUpload"))
}
