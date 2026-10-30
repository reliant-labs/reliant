package worktree

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// UpdateWorktree updates a worktree
func (s *Service) UpdateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateWorktreeRequest],
) (*connect.Response[reliantv1.UpdateWorktreeResponse], error) {
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

	if req.Msg.Name != nil {
		worktree.Name = *req.Msg.Name
	}
	if req.Msg.Status != nil {
		worktree.Status = int32(*req.Msg.Status)
	}
	if req.Msg.BaseBranch != nil {
		worktree.BaseBranch = *req.Msg.BaseBranch
	}
	now := time.Now().UTC()
	worktree.UpdatedAt = now
	worktree.LastActive = now

	if err := s.deps.Repo.UpdateWorktree(ctx, worktree); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update worktree"))
	}

	return connect.NewResponse(&reliantv1.UpdateWorktreeResponse{
		Worktree: worktreeToProto(worktree),
	}), nil
}

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
		if err := s.wakeIfAsleep(ctx, userID, machinewake.Target{DaemonID: daemonplacement.WorktreeOwner(worktree)}, project.Path); err != nil {
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
			meta := s.deps.Settler.SettleBlocking(ctx, userID, &core.ReclaimCandidate{Worktree: worktree, OwnerUserID: userID, ProjectPath: project.Path})
			if meta == nil || !meta.DirectoryDeleted {
				reason := "the machine has not removed its directory yet"
				if meta != nil && meta.HeldReason != "" {
					reason = "its directory is still on disk (" + meta.HeldReason + "): clean it up from your Inbox first"
				}
				return nil, svcerr.FailedPrecondition(fmt.Sprintf("cannot delete this workspace permanently: %s", reason))
			}
		}
		deletedBranch := false
		if req.Msg.DeleteGitBranch && project != nil && worktree.Branch != "" {
			deletedBranch = s.cleanupWorktreeBranch(ctx, userID, daemonplacement.WorktreeOwner(worktree), project.Path, worktree.Branch)
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
	if project != nil {
		s.settleArchived(ctx, userID, worktree, project.Path)
	}

	return connect.NewResponse(&reliantv1.DeleteWorktreeResponse{
		Message:           "Worktree and associated chats archived successfully",
		IsPermanentDelete: false,
	}), nil
}

// ArchiveWorktree archives a worktree (dedicated endpoint)
func (s *Service) ArchiveWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.ArchiveWorktreeRequest],
) (*connect.Response[reliantv1.ArchiveWorktreeResponse], error) {
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

	if worktree.IsMain {
		return nil, svcerr.FailedPrecondition("cannot archive the main project worktree")
	}

	if worktree.DeletedAt != nil {
		return nil, svcerr.FailedPrecondition("worktree is already archived; use delete to permanently remove")
	}

	project, err := s.deps.Repo.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		logging.WarnContext(ctx, "Failed to get project for worktree cleanup", "error", err)
	}

	// The branch is checked out in the directory until the machine removes it,
	// and git refuses to delete a checked-out branch, so deletion is requested
	// here and carried out after the machine reports the directory removed.
	s.storeCleanupMetadata(ctx, req.Msg.WorktreeId, req.Msg.DeleteGitBranch)

	// Archive the worktree
	if err := s.deps.Repo.ArchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to archive worktree"))
	}

	// Archive associated chats
	s.archiveWorktreeChats(ctx, userID, worktree)
	if project != nil {
		s.settleArchived(ctx, userID, worktree, project.Path)
	}

	return connect.NewResponse(&reliantv1.ArchiveWorktreeResponse{
		Message: "Worktree and associated chats archived successfully",
	}), nil
}

// UnarchiveWorktree restores an archived worktree
func (s *Service) UnarchiveWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.UnarchiveWorktreeRequest],
) (*connect.Response[reliantv1.UnarchiveWorktreeResponse], error) {
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

	if worktree.DeletedAt == nil {
		return nil, svcerr.FailedPrecondition("worktree is not archived")
	}

	// A directory that is already gone cannot be unarchived into a working
	// workspace: the user restores it with RecreateWorktree, which rebuilds it.
	if worktree.CleanupMetadata != nil && worktree.CleanupMetadata.DirectoryDeleted {
		return nil, svcerr.FailedPrecondition("this workspace's files were removed; restore it with recreate")
	}

	// Refuse a name collision before the machine is touched: re-locking the
	// checkouts for a row that then cannot be restored would leave them locked
	// as live under an archived row.
	if holder, err := s.deps.Repo.GetLiveWorktreeByName(ctx, worktree.ProjectID, worktree.Name); err == nil && holder != nil {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf(
			"cannot unarchive '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
	}

	// Take the archive fence off the directory FIRST, and retire it: the machine
	// re-locks the checkouts as "restored <fence>", so a removal already in
	// flight for this archive finds a lock it does not own and refuses, and no
	// claim built before now can use that fence again. See relockActive.
	proceed, err := s.relockActive(ctx, userID, worktree)
	if err != nil {
		return nil, err
	}
	if !proceed {
		return nil, svcerr.FailedPrecondition("this workspace's files were removed; restore it with recreate")
	}

	if err := s.deps.Repo.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		if errors.Is(err, core.ErrWorktreeNameTaken) {
			return nil, svcerr.FailedPrecondition(fmt.Sprintf(
				"cannot unarchive '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to unarchive worktree"))
	}

	// Unarchive associated chats
	s.unarchiveWorktreeChats(ctx, userID, worktree)

	msg := "Worktree and associated chats unarchived successfully"
	if m := worktree.CleanupMetadata; m != nil && m.StrippedBytes > 0 {
		msg += "; rebuildable files (node_modules, build output, tool caches) were removed while it was archived, so run its install/generate step before building"
	}
	return connect.NewResponse(&reliantv1.UnarchiveWorktreeResponse{
		Message: msg,
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

// storeCleanupMetadata records that the user wants the branch deleted once the
// directory is gone. Whether the directory went is the daemon's to report, so it
// is never set here, and what is already stored (held reason, snapshot refs) is
// kept.
func (s *Service) storeCleanupMetadata(ctx context.Context, worktreeID string, deleteBranch bool) {
	if !deleteBranch {
		return
	}
	err := s.deps.Repo.MergeWorktreeCleanupMetadata(ctx, worktreeID, func(m *db.CleanupMetadata, _ bool) bool {
		m.DeleteBranch = true
		return true
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to store cleanup metadata", "error", err, "worktreeID", worktreeID)
	}
}

func (s *Service) archiveWorktreeChats(ctx context.Context, userID string, worktree *db.Worktree) {
	chats, err := s.deps.Repo.ListChats(ctx, db.ChatFilters{
		UserID:          userID,
		ProjectID:       &worktree.ProjectID,
		ExcludeArchived: true,
		Limit:           1000,
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to list chats for archiving", "error", err)
		return
	}

	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktree.ID {
			// Cancel workflow if running (best-effort)
			if workflowID := chat.MainThreadID(); workflowID != "" && s.deps.Temporal != nil {
				_ = s.deps.Temporal.CancelWorkflow(ctx, workflowID, "")
			}
			if err := s.deps.Repo.UpdateChatState(ctx, chat.ID, db.ChatStateArchived, "worktree_archived"); err != nil {
				logging.WarnContext(ctx, "Failed to archive chat", "error", err, "chatID", chat.ID)
			}
		}
	}
}

func (s *Service) unarchiveWorktreeChats(ctx context.Context, userID string, worktree *db.Worktree) {
	archivedState := db.ChatStateArchived
	chats, err := s.deps.Repo.ListChats(ctx, db.ChatFilters{
		UserID:    userID,
		ProjectID: &worktree.ProjectID,
		State:     &archivedState,
		Limit:     1000,
	})
	if err != nil {
		logging.WarnContext(ctx, "Failed to list chats for unarchiving", "error", err)
		return
	}

	for _, chat := range chats {
		if chat.WorktreeID != nil && *chat.WorktreeID == worktree.ID {
			if err := s.deps.Repo.UpdateChatState(ctx, chat.ID, db.ChatStateIdle, "worktree_unarchived"); err != nil {
				logging.WarnContext(ctx, "Failed to unarchive chat", "error", err, "chatID", chat.ID)
			}
		}
	}
}

// RecreateWorktree rebuilds a worktree's directory from its branch: an
// archived worktree is restored (and unarchived); an active one whose
// directory went missing from disk is re-materialized in place, which is the
// user's way out of a chat stuck on "working directory does not exist".
func (s *Service) RecreateWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.RecreateWorktreeRequest],
) (*connect.Response[reliantv1.RecreateWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	// Check permission
	if err := s.worktreeBelongsToUser(ctx, req.Msg.WorktreeId, userID); err != nil {
		return nil, err
	}

	worktree, err := s.deps.Repo.GetWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	// Get project for git operations
	project, err := s.deps.Repo.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	if worktree.DeletedAt == nil {
		return s.repairActiveWorktree(ctx, userID, worktree, project)
	}

	// Restoring an archived worktree unarchives the row, so refuse before
	// touching the machine when its name has been taken since.
	if holder, err := s.deps.Repo.GetLiveWorktreeByName(ctx, worktree.ProjectID, worktree.Name); err == nil && holder != nil {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf(
			"cannot recreate '%s': another worktree in this project now uses that name; archive or rename the other one first", worktree.Name))
	}

	// Recreate EVERY repo of the workspace. A multi-repo project's root is not a
	// git repository, so rebuilding it means one `git worktree add` per nested
	// repo, each in its own repository, under <workspace>/<relative path>.
	repos, err := s.deps.Repo.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}
	type recreateRepo struct {
		RepoPath string `json:"repo_path"`
		Rel      string `json:"rel"`
		Branch   string `json:"branch,omitempty"`
	}
	var recreateRepos []recreateRepo
	for _, repo := range repos {
		recreateRepos = append(recreateRepos, recreateRepo{
			RepoPath: filepath.Join(project.Path, repo.RelativePath),
			Rel:      filepath.ToSlash(repo.RelativePath),
			Branch:   worktree.Branch,
		})
	}
	var recreateResp struct {
		Success      bool   `json:"success"`
		BranchExists bool   `json:"branch_exists"`
		PathExists   bool   `json:"path_exists"`
		Output       string `json:"output,omitempty"`
		Error        string `json:"error,omitempty"`
		// SnapshotApplied: saved work was put back on every checkout that had
		// some. SnapshotFailed names the ones it was not.
		SnapshotApplied bool     `json:"snapshot_applied,omitempty"`
		SnapshotFailed  []string `json:"snapshot_failed,omitempty"`
		// SnapshotBranchMoved: saved work not applied because the branch moved.
		SnapshotBranchMoved []string `json:"snapshot_branch_moved,omitempty"`
	}
	var savedRefs []string
	if worktree.CleanupMetadata != nil {
		savedRefs = worktree.CleanupMetadata.SnapshotRefs
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.recreate", map[string]any{
		"project_path":  project.Path,
		"worktree_path": worktree.Path,
		"branch":        worktree.Branch,
		"worktree_id":   worktree.ID,
		"snapshot_refs": savedRefs,
		"fence":         strconv.FormatInt(worktree.DeletedAt.UnixNano(), 10),
		"repos":         recreateRepos,
	}, &recreateResp); err != nil {
		return nil, daemonerr.Error("recreate worktree", err)
	}
	if !recreateResp.Success {
		if !recreateResp.BranchExists {
			return nil, svcerr.FailedPrecondition(fmt.Sprintf("branch '%s' no longer exists; cannot recreate worktree", worktree.Branch))
		}
		if recreateResp.PathExists {
			return nil, svcerr.AlreadyExists(fmt.Sprintf("worktree directory '%s' already exists", worktree.Path))
		}
		return nil, svcerr.Internal(fmt.Sprintf("failed to recreate worktree: %s", recreateResp.Error))
	}

	// The directory is back, so it is no longer "removed" or "held". The saved
	// refs are kept until a snapshot has actually been re-applied: they are the
	// only copy of that work, and the archived panel lists them.
	// A failed apply keeps the refs on the row, so the work is never forgotten.
	// Saved commits (".../orphans/...") are never applied, so they stay listed
	// whatever the apply did.
	keep := &db.CleanupMetadata{}
	if len(savedRefs) > 0 && !recreateResp.SnapshotApplied {
		keep.SnapshotRefs = savedRefs
	} else {
		applicable := worktreereclaim.SnapshotRefsOnRow(savedRefs)
		for _, ref := range savedRefs {
			if !slices.Contains(applicable, ref) {
				keep.SnapshotRefs = append(keep.SnapshotRefs, ref)
			}
		}
	}
	if len(keep.SnapshotRefs) == 0 {
		keep = nil
	}
	if err := s.deps.Repo.UpdateWorktreeCleanupMetadata(ctx, req.Msg.WorktreeId, keep); err != nil {
		logging.WarnContext(ctx, "Failed to update cleanup metadata", "error", err)
	}

	// Unarchive the worktree
	if err := s.deps.Repo.UnarchiveWorktree(ctx, req.Msg.WorktreeId); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to unarchive worktree"))
	}

	// Unarchive all chats associated with this worktree
	s.unarchiveWorktreeChats(ctx, userID, worktree)

	out := &reliantv1.RecreateWorktreeResponse{
		Message: "Worktree recreated successfully from branch",
		Path:    worktree.Path,
		Branch:  worktree.Branch,
	}
	switch {
	case len(recreateResp.SnapshotBranchMoved) > 0:
		out.Message = "Worktree recreated, but its saved work was not applied"
		out.SnapshotWarning = fmt.Sprintf("Your work is saved at %s but was not applied because the branch moved since it was saved.", strings.Join(savedRefs, ", "))
		out.SnapshotRefs = savedRefs
	case len(recreateResp.SnapshotFailed) > 0:
		out.Message = "Worktree recreated, but its saved work was not applied"
		out.SnapshotWarning = fmt.Sprintf("Your work is saved at %s but could not be applied (%s).", strings.Join(savedRefs, ", "), strings.Join(recreateResp.SnapshotFailed, ", "))
		out.SnapshotRefs = savedRefs
	}
	return connect.NewResponse(out), nil
}

// repairActiveWorktree asks the machine that owns an active worktree to
// recreate its directory from its branch if it has gone missing — the same
// worktree.ensure a chat's tool batch runs, so the button and the automatic
// path cannot disagree. Only the machine can see the directory, so this
// service decides nothing about it.
//
// It never moves the chat: a repair that is not possible is reported with the
// machine's reason, and where the chat runs instead is the user's call ("Move
// to main checkout", which is UpdateChat).
func (s *Service) repairActiveWorktree(ctx context.Context, userID string, worktree *db.Worktree, project *db.Project) (*connect.Response[reliantv1.RecreateWorktreeResponse], error) {
	if worktree.IsMain {
		return nil, svcerr.FailedPrecondition("the project's main checkout is not a workspace reliant can recreate")
	}
	if worktree.Path == "" {
		return nil, svcerr.FailedPrecondition("this workspace was never created on disk; create a new one instead")
	}
	repos, err := s.deps.Repo.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}
	ensureReq := toolexec.WorkspaceEnsureRequest{
		Path:         worktree.Path,
		Repair:       true,
		WorktreeID:   worktree.ID,
		Branch:       worktree.Branch,
		Repos:        toolexec.WorkspaceReposOf(project.Path, repos),
		FallbackPath: project.Path,
	}
	var resp toolexec.WorkspaceEnsureResponse
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, daemonplacement.WorktreeOwner(worktree), toolexec.WorkspaceEnsureCommand, ensureReq, &resp, toolexec.WorkspaceEnsureTimeoutMs); err != nil {
		if machinewake.IsWaking(err) {
			return nil, err
		}
		return nil, daemonerr.Error("repair the workspace", err)
	}

	out := &reliantv1.RecreateWorktreeResponse{Path: worktree.Path, Branch: worktree.Branch}
	switch resp.Status {
	case toolexec.WorkspacePresent:
		out.Message = "The workspace is on disk; nothing needed recreating"
	case toolexec.WorkspaceRepaired:
		out.Message = fmt.Sprintf("Workspace recreated from branch %s. Uncommitted changes that were in it are gone.", worktree.Branch)
	default:
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("could not recreate the workspace: %s", resp.Detail))
	}
	s.emitWorktreeChanged(ctx, userID, project.ID, worktree.ID)
	return connect.NewResponse(out), nil
}
