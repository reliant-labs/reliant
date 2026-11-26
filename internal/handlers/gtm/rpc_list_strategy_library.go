package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListStrategyLibrary lists the org's strategy library, by category then name.
func (s *Service) ListStrategyLibrary(
	ctx context.Context,
	req *connect.Request[pb.ListStrategyLibraryRequest],
) (*connect.Response[pb.ListStrategyLibraryResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListStrategyLibrary(ctx, a, gtmsvc.ListStrategyLibraryInput{
		Category:   enumIn[gtmsvc.StrategyCategory](m.GetCategory()),
		Depth:      enumIn[gtmsvc.LibraryDepth](m.GetDepth()),
		Stage:      m.GetStage(),
		Cost:       m.GetCost(),
		Difficulty: m.GetDifficulty(),
		Fit:        m.GetFit(),
		Query:      m.GetQuery(),
		PageSize:   int(m.GetPageSize()),
		PageToken:  m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListStrategyLibraryResponse{
		Entries:       libraryEntriesOut(res.Entries),
		NextPageToken: res.NextPageToken,
	}), nil
}
