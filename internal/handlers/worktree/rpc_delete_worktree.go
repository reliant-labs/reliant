package worktree

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
)

// DeleteWorktree deletes or archives a worktree based on its current state
func (s *Service) DeleteWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteWorktreeRequest],
) (*connect.Response[reliantv1.DeleteWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.deps.Repo.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	// Prevent deletion of main worktree
	if worktree.IsMain {
		return nil, svcerr.FailedPrecondition("cannot delete the main project worktree")
	}

	project, err := s.deps.Repo.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to get project for worktree cleanup", "error", err)
	}

	isPermanentDelete := worktree.DeletedAt != nil

	// The directory is no longer the client's call: the owning daemon removes it
	// when it is provably safe and otherwise holds it for the user to clean up
	// knowingly. A permanent delete and a branch delete still reach the machine
	// from here, so wake an asleep one first; the client retries.
	if (req.Msg.DeleteGitBranch || isPermanentDelete) && project != nil {
		if err := s.wakeIfAsleep(ctx, userID, machinewake.Target{DaemonID: daemonplacement.WorktreeOwner(worktree)}, worktree.Path); err != nil {
			return nil, err
		}
	}

	// The directory is no longer the client's call. The owning daemon removes
	// it when it is provably safe (clean, pushed or merged, not in use) and
	// otherwise holds it for the user to clean up knowingly; see settleArchived.
	if isPermanentDelete {
		// The row is the only thing that lets reliant find this directory
		// again, so it may only go once the machine has removed the directory
		// (or it never existed). Otherwise the directory stays locked with
		// reliant's reason forever and nothing will ever clean it up.
		if worktree.Path != "" && project != nil && s.deps.Settler != nil {
			// The settle runs git in the owner's checkout of the project; an
			// owner without one cannot remove the directory, and says why.
			checkout, err := s.checkoutOn(ctx, userID, project, daemonplacement.WorktreeOwner(worktree), "delete this workspace permanently")
			if err != nil {
				return nil, err
			}
			meta := s.deps.Settler.SettleBlocking(ctx, userID, &core.ReclaimCandidate{Worktree: worktree, OwnerUserID: userID, ProjectPath: checkout.Path})
			if meta == nil || !meta.DirectoryDeleted {
				reason := "the machine has not removed its directory yet"
				if meta != nil && meta.HeldReason != "" {
					reason = "its directory is still on disk (" + meta.HeldReason + "): clean it up from your Inbox first"
				}
				return nil, svcerr.FailedPrecondition(fmt.Sprintf("cannot delete this workspace permanently: %s", reason))
			}
		}
		deletedBranch := false
		if req.Msg.DeleteGitBranch && worktree.Branch != "" {
			if checkout := s.ownerCheckout(ctx, userID, project, worktree); checkout != "" {
				deletedBranch = s.cleanupWorktreeBranch(ctx, userID, daemonplacement.WorktreeOwner(worktree), checkout, worktree.Branch)
			}
		}
		if err := s.deps.Repo.DeleteWorktree(ctx, req.Msg.WorktreeId); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to permanently delete worktree"))
		}

		return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
			Message:           "Worktree permanently deleted",
			DeletedBranch:     deletedBranch,
			IsPermanentDelete: true,
		}), nil
	}

	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, req.Msg.DeleteGitBranch)

	// Archive the worktree
	if err := s.deps.Repo.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)
	s.settleArchived(ctx, userID, worktree, s.ownerCheckout(ctx, userID, project, worktree))

	return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
		Message:           "Worktree and associated chats archived successfully",
		IsPermanentDelete: false,
	}), nil
}

// =============================================================================
// Cleanup Helpers
// =============================================================================

// cleanupWorktreeBranch deletes the worktree's branch from the project clone on
// daemonID — the owner's clone, which is where creating the worktree made it.
func (s *Service) cleanupWorktreeBranch(ctx context.Context, userID, daemonID, projectPath, branch string) bool {
	var resp struct {
		Deleted bool `json:"deleted"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.delete_branch", map[string]string{
		"project_path": projectPath,
		"branch":       branch,
	}, &resp); err != nil {
		logging.WarnContext(ctx, "Failed to delete git branch via daemon", "error", err, "branch", branch)
		return false
	}
	return resp.Deleted
}
