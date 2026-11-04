package inbox

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// ForgetStorageWorktree drops the record of the folder of an archived
// workspace no machine is on record for, once no connected machine has it
// (worktreesweep.Forget). Nothing on any disk is deleted.
func (s *Service) ForgetStorageWorktree(
	ctx context.Context,
	req *connect.Request[pb.ForgetStorageWorktreeRequest],
) (*connect.Response[pb.ForgetStorageWorktreeResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}
	err := worktreesweep.Forget(ctx, s.deps.Repo, userID, req.Msg.WorktreeId)
	switch {
	case errors.Is(err, worktreesweep.ErrNotHeld):
		return nil, svcerr.NotFound("held workspace")
	case errors.Is(err, worktreesweep.ErrNotForgettable):
		return nil, svcerr.FailedPrecondition("it can be forgotten only while no machine is on record for it " +
			"and every connected machine has been checked and does not have its folder; " +
			"connect a machine, or wait for the next check, and try again")
	case err != nil:
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not forget the workspace"))
	}
	s.storageCache.Delete(userID)
	return connect.NewResponse(&pb.ForgetStorageWorktreeResponse{}), nil
}
