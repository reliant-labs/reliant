package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetAssetDownload returns a short-lived URL for a READY asset's bytes.
func (s *Service) GetAssetDownload(
	ctx context.Context,
	req *connect.Request[pb.GetAssetDownloadRequest],
) (*connect.Response[pb.GetAssetDownloadResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	dl, err := s.deps.Service.GetAssetDownload(ctx, a, gtmsvc.GetAssetDownloadInput{AssetID: m.GetAssetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetAssetDownloadResponse{
		Url:       dl.URL,
		ExpiresAt: timeOut(dl.ExpiresAt),
	}), nil
}
