package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// DeleteAsset deletes an asset in any status, row and objects: a person's
// call.
func (s *Service) DeleteAsset(
	ctx context.Context,
	req *connect.Request[pb.DeleteAssetRequest],
) (*connect.Response[pb.DeleteAssetResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Service.DeleteAsset(ctx, a, gtmsvc.DeleteAssetInput{AssetID: m.GetAssetId()}); err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.DeleteAssetResponse{}), nil
}
