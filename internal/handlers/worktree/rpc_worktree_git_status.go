package worktree

import (
	"context"
	"path/filepath"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/logging"
)

// =============================================================================
// Git Read Operations
// =============================================================================

// GetWorktreeChanges gets file changes for a worktree
func (s *Service) GetWorktreeChanges(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeChangesRequest],
) (*connect.Response[reliantv1.GetWorktreeChangesResponse], error) {
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

	// Validate worktree is suitable for git operations
	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Get changes via daemon
	type fileChangeEntry struct {
		Path     string `json:"path"`
		Status   string `json:"status"`
		IsNew    bool   `json:"is_new"`
		Diff     string `json:"diff"`
		IsBinary bool   `json:"is_binary"`
	}
	var changesResp struct {
		Branch        string            `json:"branch"`
		Files         []fileChangeEntry `json:"files"`
		TotalFiles    int32             `json:"total_files"`
		Ahead         int32             `json:"ahead"`
		Behind        int32             `json:"behind"`
		DefaultBranch string            `json:"default_branch"`
		Error         string            `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, s.owner(ctx, userID, worktree), "worktree.git_changes", map[string]string{
		"worktree_path": repoPath,
		"branch":        worktree.Branch,
		"base_branch":   worktree.BaseBranch,
	}, &changesResp, worktreeReadCommandTimeoutMs); err != nil {
		return nil, daemonerr.Error("get worktree changes", err)
	}
	if changesResp.Error != "" {
		// The daemon's own report of what git said.
		return nil, svcerr.Internal(changesResp.Error)
	}

	// Convert to proto
	files := make([]*reliantv1.WorktreeFileChange, len(changesResp.Files))
	for i, f := range changesResp.Files {
		var status reliantv1.FileChangeStatus
		switch f.Status {
		case "untracked":
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_UNTRACKED
		case "staged":
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_STAGED
		case "modified":
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_MODIFIED
		default:
			status = reliantv1.FileChangeStatus_FILE_CHANGE_STATUS_MODIFIED
		}
		files[i] = &reliantv1.WorktreeFileChange{
			Path:     f.Path,
			Status:   status,
			IsNew:    f.IsNew,
			Diff:     f.Diff,
			IsBinary: f.IsBinary,
		}
	}

	return connect.NewResponse(&reliantv1.GetWorktreeChangesResponse{
		Branch:        changesResp.Branch,
		Files:         files,
		TotalFiles:    changesResp.TotalFiles,
		Ahead:         changesResp.Ahead,
		Behind:        changesResp.Behind,
		DefaultBranch: changesResp.DefaultBranch,
	}), nil
}

// GetWorktreeGitStatus gets the overall git status for a worktree
func (s *Service) GetWorktreeGitStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeGitStatusRequest],
) (*connect.Response[reliantv1.GetWorktreeGitStatusResponse], error) {
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

	// Validate worktree is suitable for git operations
	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Get git status via daemon
	var statusResp struct {
		Branch         string   `json:"branch"`
		HasChanges     bool     `json:"has_changes"`
		Status         string   `json:"status"`
		StagedFiles    []string `json:"staged_files"`
		UnstagedFiles  []string `json:"unstaged_files"`
		UntrackedFiles []string `json:"untracked_files"`
		Ahead          int32    `json:"ahead"`
		Behind         int32    `json:"behind"`
	}
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, s.owner(ctx, userID, worktree), "worktree.git_status", map[string]string{
		"worktree_path": repoPath,
		"branch":        worktree.Branch,
	}, &statusResp, worktreeReadCommandTimeoutMs); err != nil {
		return nil, daemonerr.Error("get git status", err)
	}

	return connect.NewResponse(&reliantv1.GetWorktreeGitStatusResponse{
		WorktreeId:     worktree.ID,
		Path:           repoPath,
		Branch:         statusResp.Branch,
		Clean:          statusResp.Status == "clean",
		HasChanges:     statusResp.HasChanges,
		StagedFiles:    statusResp.StagedFiles,
		ModifiedFiles:  statusResp.UnstagedFiles,
		UntrackedFiles: statusResp.UntrackedFiles,
		Ahead:          statusResp.Ahead,
		Behind:         statusResp.Behind,
	}), nil
}

// GetWorktreeCommits gets commit history for a worktree
func (s *Service) GetWorktreeCommits(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreeCommitsRequest],
) (*connect.Response[reliantv1.GetWorktreeCommitsResponse], error) {
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

	// Validate worktree is suitable for git operations
	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Get commit limit (default 20, max 100)
	limit := int32(20)
	if req.Msg.Limit > 0 && req.Msg.Limit <= 100 {
		limit = req.Msg.Limit
	}

	// Determine base branch
	baseBranch := worktree.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Get commits via daemon
	type commitEntry struct {
		Hash      string `json:"hash"`
		ShortHash string `json:"short_hash"`
		Author    string `json:"author"`
		Email     string `json:"email"`
		Date      string `json:"date"`
		Message   string `json:"message"`
	}
	var commitsResp struct {
		Commits        []commitEntry `json:"commits"`
		Total          int32         `json:"total"`
		Branch         string        `json:"branch"`
		BaseBranch     string        `json:"base_branch"`
		ComparisonMode bool          `json:"comparison_mode"`
		ComparisonRef  string        `json:"comparison_ref"`
		CurrentBranch  string        `json:"current_branch"`
	}
	type commitsReq struct {
		WorktreePath string `json:"worktree_path"`
		Branch       string `json:"branch"`
		BaseBranch   string `json:"base_branch"`
		Limit        int32  `json:"limit"`
	}
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, s.owner(ctx, userID, worktree), "worktree.git_commits", commitsReq{
		WorktreePath: repoPath,
		Branch:       worktree.Branch,
		BaseBranch:   baseBranch,
		Limit:        limit,
	}, &commitsResp, worktreeReadCommandTimeoutMs); err != nil {
		logging.WarnContext(ctx, "Failed to get git commits via daemon", "error", err)
		return connect.NewResponse(&reliantv1.GetWorktreeCommitsResponse{
			Commits: []*reliantv1.GitCommit{},
		}), nil
	}

	// Convert to proto
	commits := make([]*reliantv1.GitCommit, len(commitsResp.Commits))
	for i, c := range commitsResp.Commits {
		commits[i] = &reliantv1.GitCommit{
			Hash:      c.Hash,
			ShortHash: c.ShortHash,
			Author:    c.Author,
			Email:     c.Email,
			Date:      c.Date,
			Message:   c.Message,
		}
	}

	return connect.NewResponse(&reliantv1.GetWorktreeCommitsResponse{
		Commits:        commits,
		Total:          commitsResp.Total,
		Branch:         worktree.Branch,
		BaseBranch:     worktree.BaseBranch,
		ComparisonMode: commitsResp.ComparisonMode,
		ComparisonRef:  commitsResp.ComparisonRef,
		CurrentBranch:  commitsResp.CurrentBranch,
	}), nil
}

// ListWorktreeRepoStatuses returns per-repo git status across every nested
// repo in the worktree's project. Drives the right-sidebar grouped view.
//
// This fans worktree.git_status N times (one per repo) under the workspace
// root. Per-repo failures don't abort the response; the row carries an
// error string so the UI can render "couldn't read this one." Single-repo
// and zero-repo projects collapse to a single-element (or empty) response.
func (s *Service) ListWorktreeRepoStatuses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListWorktreeRepoStatusesRequest],
) (*connect.Response[reliantv1.ListWorktreeRepoStatusesResponse], error) {
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

	if err := s.validateWorktreeForGitOps(ctx, userID, worktree); err != nil {
		return nil, err
	}

	repos, err := s.deps.Repo.ListReposByProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
	}

	// 0-repo project: empty response. The UI treats this as "nothing to show
	// in the sidebar" rather than an error so newly-created projects without
	// any nested repo registered yet still render cleanly.
	if len(repos) == 0 {
		return connect.NewResponse(&reliantv1.ListWorktreeRepoStatusesResponse{
			Statuses: []*reliantv1.WorktreeRepoStatus{},
		}), nil
	}

	statuses := make([]*reliantv1.WorktreeRepoStatus, 0, len(repos))
	for _, r := range repos {
		repoPath := filepath.Join(worktree.Path, r.RelativePath)
		row := &reliantv1.WorktreeRepoStatus{
			RepoId:           r.ID,
			RepoName:         r.Name,
			RepoRelativePath: r.RelativePath,
		}

		var statusResp struct {
			Branch         string   `json:"branch"`
			HasChanges     bool     `json:"has_changes"`
			Status         string   `json:"status"`
			StagedFiles    []string `json:"staged_files"`
			UnstagedFiles  []string `json:"unstaged_files"`
			UntrackedFiles []string `json:"untracked_files"`
			Ahead          int32    `json:"ahead"`
			Behind         int32    `json:"behind"`
			Error          string   `json:"error,omitempty"`
		}
		if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, s.owner(ctx, userID, worktree), "worktree.git_status", map[string]string{
			"worktree_path": repoPath,
			"branch":        worktree.Branch,
		}, &statusResp, worktreeReadCommandTimeoutMs); err != nil {
			row.Error = err.Error()
			statuses = append(statuses, row)
			continue
		}

		row.CurrentBranch = statusResp.Branch
		row.HasChanges = statusResp.HasChanges
		row.Ahead = statusResp.Ahead
		row.Behind = statusResp.Behind
		row.ChangedFiles = int32(len(statusResp.StagedFiles) + len(statusResp.UnstagedFiles) + len(statusResp.UntrackedFiles))
		if statusResp.Error != "" {
			row.Error = statusResp.Error
		}
		statuses = append(statuses, row)
	}

	return connect.NewResponse(&reliantv1.ListWorktreeRepoStatusesResponse{
		Statuses: statuses,
	}), nil
}
