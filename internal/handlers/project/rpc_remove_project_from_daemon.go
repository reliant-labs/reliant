package project

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// RemoveProjectFromDaemon deletes the on-disk clone AND the
// project_daemons row. The delete is pinned to that daemon: sent by default
// resolution, it removed whatever sat at the same path on another machine. The Project row itself is untouched (it may still be
// cloned on other daemons). Idempotent — calling on an absent clone still
// succeeds so the UI can clean up half-broken state.
func (s *Service) RemoveProjectFromDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.RemoveProjectFromDaemonRequest],
) (*connect.Response[reliantv1.RemoveProjectFromDaemonResponse], error) {
	userID := auth.MustGetUserID(ctx)
	project, pd, err := s.resolveProjectDaemonRow(ctx, req.Msg.ProjectId, req.Msg.DaemonId, userID)
	if err != nil {
		// NotFound is treated as success — the row's already gone and
		// there's nothing on disk to delete from our perspective. Anything
		// else is a real error.
		if svcerr.IsNotFound(err) {
			return connect.NewResponse(&reliantv1.RemoveProjectFromDaemonResponse{}), nil
		}
		return nil, err
	}
	if err := s.removeCheckout(ctx, userID, project, pd); err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.RemoveProjectFromDaemonResponse{}), nil
}
