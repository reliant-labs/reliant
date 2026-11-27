package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// CancelLibraryPrune records a PENDING prune request CANCELLED, removing
// nothing. Refused as ConfirmLibraryPrune is: a person-class credential only.
func (s *Service) CancelLibraryPrune(
	ctx context.Context,
	req *connect.Request[pb.CancelLibraryPruneRequest],
) (*connect.Response[pb.CancelLibraryPruneResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	r, err := s.deps.Service.CancelLibraryPrune(ctx, a, gtmsvc.CancelLibraryPruneInput{RequestID: m.GetRequestId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.CancelLibraryPruneResponse{Request: pruneRequestOut(a.OrgID, r)}), nil
}
