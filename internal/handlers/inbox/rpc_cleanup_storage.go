package inbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// CleanupStorage runs the storage item's Clean up action on one machine.
func (s *Service) CleanupStorage(
	ctx context.Context,
	req *reliantv1.CleanupStorageRequest,
) (*reliantv1.CleanupStorageResponse, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if req.DaemonId == "" || len(req.WorktreeIds) == 0 {
		return nil, svcerr.InvalidArgument("daemon_id and worktree_ids are required")
	}
	if len(req.WorktreeIds) > maxInboxDismissBatch {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("at most %d worktrees per call", maxInboxDismissBatch))
	}
	if s.deps.Sweeper == nil {
		return nil, svcerr.Unavailable("storage cleanup is not available")
	}
	// The machine must be the caller's: Cleanup only ever acts on worktrees of
	// projects the caller owns, but naming a stranger's daemon is rejected
	// outright rather than quietly doing nothing.
	daemon, err := s.deps.Repo.GetDaemon(ctx, req.DaemonId)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get machine"))
	}
	if err != nil || !authz.CanWriteMachine(ctx, userID, daemon) {
		return nil, svcerr.NotFound("machine")
	}

	accepted, skipped, err := s.deps.Sweeper.StartCleanup(ctx, s.deps.Repo, userID, req.DaemonId, req.WorktreeIds)
	if errors.Is(err, worktreesweep.ErrOffline) {
		return nil, svcerr.FailedPrecondition("that machine is offline; reconnect it and try again")
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("storage cleanup failed"))
	}

	s.storageCache.Delete(userID)
	resp := &reliantv1.CleanupStorageResponse{}
	for _, id := range accepted {
		resp.Results = append(resp.Results, &reliantv1.CleanupStorageResult{
			WorktreeId: id, Outcome: reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_ACCEPTED,
		})
	}
	for _, r := range skipped {
		resp.Results = append(resp.Results, &reliantv1.CleanupStorageResult{
			WorktreeId: r.WorktreeID, Outcome: reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_SKIPPED, Message: r.Message,
		})
	}
	if len(accepted) == 0 {
		activity.Skip(ctx) // every worktree was skipped: nothing is being cleaned up
	}
	return resp, nil
}
