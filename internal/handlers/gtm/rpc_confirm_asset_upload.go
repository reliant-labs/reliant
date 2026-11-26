package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ConfirmAssetUpload finishes an upload: the domain checks the bytes and
// marks the asset READY.
func (s *Service) ConfirmAssetUpload(
	ctx context.Context,
	req *connect.Request[pb.ConfirmAssetUploadRequest],
) (*connect.Response[pb.ConfirmAssetUploadResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	asset, err := s.deps.Service.ConfirmAssetUpload(ctx, a, gtmsvc.ConfirmAssetUploadInput{AssetID: m.GetAssetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ConfirmAssetUploadResponse{Asset: assetOut(asset)}), nil
}
