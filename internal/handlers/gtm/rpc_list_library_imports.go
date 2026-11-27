package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListLibraryImports lists the org's strategy library import audit, newest
// first.
func (s *Service) ListLibraryImports(
	ctx context.Context,
	req *connect.Request[pb.ListLibraryImportsRequest],
) (*connect.Response[pb.ListLibraryImportsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListLibraryImports(ctx, a, gtmsvc.ListLibraryImportsInput{
		PageSize:  int(m.GetPageSize()),
		PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListLibraryImportsResponse{
		Imports:       libraryImportsOut(a.OrgID, res.Imports),
		NextPageToken: res.NextPageToken,
	}), nil
}
