package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

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

	worktree, err := s.getWorktree(ctx, req.Msg.WorktreeId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get worktree"))
	}

	// Get project for git operations
	project, err := s.getProject(ctx, worktree.ProjectID)
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

	// The repos the workspace is rebuilt from are the owner's checkout.
	checkout, err := s.checkoutOn(ctx, userID, project, daemonplacement.WorktreeOwner(worktree), "recreate its workspaces")
	if err != nil {
		return nil, err
	}

	// Recreate EVERY repo of the workspace. A multi-repo project's root is not a
	// git repository, so rebuilding it means one `git worktree add` per nested
	// repo, each in its own repository, under <workspace>/<relative path>.
	repos, err := s.listRepos(ctx, worktree.ProjectID)
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
			RepoPath: checkout.Join(repo.RelativePath),
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
	if err := s.sendWorktreeDaemonCommand(ctx, userID, checkout.DaemonID, "worktree.recreate", map[string]any{
		"project_path":  checkout.Path,
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
	checkout, err := s.checkoutOn(ctx, userID, project, daemonplacement.WorktreeOwner(worktree), "recreate its workspaces")
	if err != nil {
		return nil, err
	}
	repos, err := s.listRepos(ctx, worktree.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}
	ensureReq := toolexec.WorkspaceEnsureRequest{
		Path:         worktree.Path,
		Repair:       true,
		WorktreeID:   worktree.ID,
		Branch:       worktree.Branch,
		Repos:        toolexec.WorkspaceReposOf(checkout.Path, repos),
		FallbackPath: checkout.Path,
	}
	var resp toolexec.WorkspaceEnsureResponse
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, checkout.DaemonID, toolexec.WorkspaceEnsureCommand, ensureReq, &resp, toolexec.WorkspaceEnsureTimeoutMs); err != nil {
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
