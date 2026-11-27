package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// ConfirmLibraryPrune carries out a PENDING prune request a token's import
// staged. Only a person-class credential may: the caller is a Person only
// through one (actor), and the domain refuses anyone else, an access token
// included, PermissionDenied.
func (s *Service) ConfirmLibraryPrune(
	ctx context.Context,
	req *connect.Request[pb.ConfirmLibraryPruneRequest],
) (*connect.Response[pb.ConfirmLibraryPruneResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	res, err := s.deps.Service.ConfirmLibraryPrune(ctx, a, gtmsvc.ConfirmLibraryPruneInput{RequestID: m.GetRequestId()})
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.ConfirmLibraryPruneResponse{
		Removed: count32(res.Removed),
		Request: pruneRequestOut(a.OrgID, res.Request),
	}), nil
}
