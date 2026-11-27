package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CreateAssetUpload starts an upload: a PENDING asset and a presigned PUT of
// its bytes (Unavailable while no asset store is configured).
func (s *Service) CreateAssetUpload(
	ctx context.Context,
	req *connect.Request[pb.CreateAssetUploadRequest],
) (*connect.Response[pb.CreateAssetUploadResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	up, err := s.deps.Service.CreateAssetUpload(ctx, a, gtmsvc.CreateAssetUploadInput{
		ItemID:      m.GetItemId(),
		CampaignID:  m.GetCampaignId(),
		Filename:    m.GetFilename(),
		ContentType: m.GetContentType(),
		SizeBytes:   m.GetSizeBytes(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CreateAssetUploadResponse{
		Asset:           assetOut(up.Asset),
		UploadUrl:       up.UploadURL,
		RequiredHeaders: up.RequiredHeaders,
		ExpiresAt:       timeOut(up.ExpiresAt),
	}), nil
}
