package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ListLibraryPruneRequests lists the org's staged library prunes, newest
// first; a PENDING one past its week is EXPIRED.
func (s *Service) ListLibraryPruneRequests(
	ctx context.Context,
	req *connect.Request[pb.ListLibraryPruneRequestsRequest],
) (*connect.Response[pb.ListLibraryPruneRequestsResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ListLibraryPruneRequests(ctx, a, gtmsvc.ListLibraryPruneRequestsInput{
		Status:    enumIn[gtmsvc.PruneRequestStatus](m.GetStatus()),
		PageSize:  int(m.GetPageSize()),
		PageToken: m.GetPageToken(),
	})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ListLibraryPruneRequestsResponse{
		Requests:      pruneRequestsOut(a.OrgID, res.Requests),
		NextPageToken: res.NextPageToken,
	}), nil
}
