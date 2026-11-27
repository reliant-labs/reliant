package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetAsset returns one asset's metadata.
func (s *Service) GetAsset(
	ctx context.Context,
	req *connect.Request[pb.GetAssetRequest],
) (*connect.Response[pb.GetAssetResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	asset, err := s.deps.Service.GetAsset(ctx, a, gtmsvc.GetAssetInput{ID: m.GetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetAssetResponse{Asset: assetOut(asset)}), nil
}
