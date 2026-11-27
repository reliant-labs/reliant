package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// GetStrategyLibraryEntry returns one library entry by key.
func (s *Service) GetStrategyLibraryEntry(
	ctx context.Context,
	req *connect.Request[pb.GetStrategyLibraryEntryRequest],
) (*connect.Response[pb.GetStrategyLibraryEntryResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	e, err := s.deps.Service.GetStrategyLibraryEntry(ctx, a, gtmsvc.GetStrategyLibraryEntryInput{Key: m.GetKey()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.GetStrategyLibraryEntryResponse{Entry: libraryEntryOut(e)}), nil
}
