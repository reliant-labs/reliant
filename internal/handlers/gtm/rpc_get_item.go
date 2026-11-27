package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetItem returns one item of the org.
func (s *Service) GetItem(
	ctx context.Context,
	req *connect.Request[pb.GetItemRequest],
) (*connect.Response[pb.GetItemResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	item, err := s.deps.Service.GetItem(ctx, a, gtmsvc.GetItemInput{ID: m.GetId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetItemResponse{Item: itemOut(item)}), nil
}
