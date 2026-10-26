package worktree

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/logging"
)

// =============================================================================
// Git Write Operations
// =============================================================================

// StageFiles stages files in a worktree
func (s *Service) StageFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.StageFilesRequest],
) (*connect.Response[reliantv1.StageFilesResponse], error) {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Stage files via daemon
	type stageReq struct {
		WorktreePath string   `json:"worktree_path"`
		Files        []string `json:"files"`
	}
	var stageResp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.stage", stageReq{
		WorktreePath: repoPath,
		Files:        req.Msg.Files,
	}, &stageResp); err != nil {
		return nil, daemonerr.Error("stage files", err)
	}
	if !stageResp.Success {
		return nil, svcerr.Internal(fmt.Sprintf("failed to stage files: %s", stageResp.Error))
	}

	return connect.NewResponse(&reliantv1.StageFilesResponse{
		Message: "Files staged successfully",
		Files:   req.Msg.Files,
	}), nil
}

// UnstageFiles unstages files in a worktree
func (s *Service) UnstageFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.UnstageFilesRequest],
) (*connect.Response[reliantv1.UnstageFilesResponse], error) {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Unstage files via daemon
	type unstageReq struct {
		WorktreePath string   `json:"worktree_path"`
		Files        []string `json:"files"`
	}
	var unstageResp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.unstage", unstageReq{
		WorktreePath: repoPath,
		Files:        req.Msg.Files,
	}, &unstageResp); err != nil {
		return nil, daemonerr.Error("unstage files", err)
	}
	if !unstageResp.Success {
		return nil, svcerr.Internal(fmt.Sprintf("failed to unstage files: %s", unstageResp.Error))
	}

	return connect.NewResponse(&reliantv1.UnstageFilesResponse{
		Message: "Files unstaged successfully",
		Files:   req.Msg.Files,
	}), nil
}

// CommitWorktree commits staged changes in a worktree via daemon
func (s *Service) CommitWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.CommitWorktreeRequest],
) (*connect.Response[reliantv1.CommitWorktreeResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if req.Msg.Message == "" {
		return nil, svcerr.InvalidArgument("commit message is required")
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Commit via daemon
	output, err := s.commitViaDaemon(ctx, userID, daemonplacement.WorktreeOwner(worktree), repoPath, req.Msg.Message)
	if err != nil {
		// err's text is the client's: git's words, or the machine's state.
		errStr := err.Error()
		if strings.Contains(errStr, "nothing to commit") || strings.Contains(errStr, "nothing added to commit") {
			return nil, svcerr.WithCause(svcerr.FailedPrecondition("no staged changes to commit; stage files first"), err)
		}
		return nil, err
	}

	return connect.NewResponse(&reliantv1.CommitWorktreeResponse{
		Message: "Changes committed successfully",
		Output:  output,
	}), nil
}

// PushWorktree pushes changes from a worktree to remote
func (s *Service) PushWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.PushWorktreeRequest],
) (*connect.Response[reliantv1.PushWorktreeResponse], error) {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Push via daemon. Branch is resolved daemon-side from HEAD, not from
	// worktree.Branch — the user may have checked out a different branch in
	// this repo since creation.
	output, err := s.pushViaDaemon(ctx, userID, daemonplacement.WorktreeOwner(worktree), repoPath)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PushWorktreeResponse{
		Message: "Changes pushed successfully",
		Output:  output,
	}), nil
}

// PullWorktree pulls changes from remote
func (s *Service) PullWorktree(
	ctx context.Context,
	req *connect.Request[reliantv1.PullWorktreeRequest],
) (*connect.Response[reliantv1.PullWorktreeResponse], error) {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Pull via daemon. Branch resolved daemon-side from HEAD.
	output, err := s.pullViaDaemon(ctx, userID, daemonplacement.WorktreeOwner(worktree), repoPath)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.PullWorktreeResponse{
		Message: "Changes pulled successfully",
		Output:  output,
	}), nil
}

// GetWorktreePR checks if a PR already exists for the worktree's branch
func (s *Service) GetWorktreePR(
	ctx context.Context,
	req *connect.Request[reliantv1.GetWorktreePRRequest],
) (*connect.Response[reliantv1.GetWorktreePRResponse], error) {
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Get PR info via daemon
	var prResp struct {
		Exists     bool   `json:"exists"`
		URL        string `json:"url,omitempty"`
		Number     int32  `json:"number,omitempty"`
		Title      string `json:"title,omitempty"`
		State      string `json:"state,omitempty"`
		LocalHead  string `json:"local_head,omitempty"`
		HeadRefOid string `json:"head_ref_oid,omitempty"`
	}
	// Branch is resolved daemon-side from HEAD, not from worktree.Branch.
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.get_pr", map[string]string{
		"worktree_path": repoPath,
	}, &prResp); err != nil {
		logging.WarnContext(ctx, "Failed to check PR via daemon", "error", err)
		return connect.NewResponse(&reliantv1.GetWorktreePRResponse{Exists: false}), nil
	}

	if !prResp.Exists {
		return connect.NewResponse(&reliantv1.GetWorktreePRResponse{Exists: false}), nil
	}

	// If the PR is not OPEN, check if commits match
	if prResp.State != "OPEN" {
		if prResp.LocalHead != "" && prResp.HeadRefOid != "" && prResp.LocalHead != prResp.HeadRefOid {
			return connect.NewResponse(&reliantv1.GetWorktreePRResponse{Exists: false}), nil
		}
	}

	return connect.NewResponse(&reliantv1.GetWorktreePRResponse{
		Exists: true,
		Url:    &prResp.URL,
		Number: &prResp.Number,
		Title:  &prResp.Title,
		State:  &prResp.State,
	}), nil
}

// CreateWorktreePR creates a pull request for a worktree
// It automatically stages, commits, and pushes changes if needed for a seamless PR experience
func (s *Service) CreateWorktreePR(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateWorktreePRRequest],
) (*connect.Response[reliantv1.CreateWorktreePRResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if req.Msg.Title == "" {
		return nil, svcerr.InvalidArgument("PR title is required")
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

	repoPath, repo, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Route entire create-PR flow through daemon (stage, commit, push, create PR).
	// The daemon resolves the source branch from HEAD on repoPath — worktree.Branch
	// is the creation-time branch and may diverge per-repo after the user checks
	// out something else. We *do* still pass base_branch: it's persisted at
	// create time per-repo (worktree.BaseBranches[repo_id]) so we can honor
	// non-default bases like master/develop/release.
	body := ""
	if req.Msg.Body != nil && *req.Msg.Body != "" {
		body = *req.Msg.Body
	}

	baseBranch := s.resolveRepoBaseBranch(worktree, repo)

	var prResp struct {
		Success       bool   `json:"success"`
		PRURL         string `json:"pr_url,omitempty"`
		Output        string `json:"output,omitempty"`
		AutoCommitted bool   `json:"auto_committed"`
		AutoPushed    bool   `json:"auto_pushed"`
		Error         string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.create_pr", map[string]string{
		"worktree_path": repoPath,
		"title":         req.Msg.Title,
		"body":          body,
		"base_branch":   baseBranch,
	}, &prResp); err != nil {
		return nil, daemonerr.Error("create PR", err)
	}

	if !prResp.Success {
		// The daemon's own report of what git / gh said.
		errMsg := prResp.Error
		if strings.Contains(errMsg, "cannot create a pull request from the default branch") {
			return nil, svcerr.FailedPrecondition(errMsg)
		}
		return nil, svcerr.Internal(errMsg)
	}

	// Build informative message
	message := "Pull request created successfully"
	if prResp.AutoCommitted && prResp.AutoPushed {
		message = "Changes committed and pushed, pull request created successfully"
	} else if prResp.AutoCommitted {
		message = "Changes committed, pull request created successfully"
	} else if prResp.AutoPushed {
		message = "Branch pushed, pull request created successfully"
	}

	return connect.NewResponse(&reliantv1.CreateWorktreePRResponse{
		Message:       message,
		PrUrl:         prResp.PRURL,
		Output:        prResp.Output,
		AutoCommitted: prResp.AutoCommitted,
		AutoPushed:    prResp.AutoPushed,
	}), nil
}

// RevertFiles reverts/discards file changes via daemon
func (s *Service) RevertFiles(
	ctx context.Context,
	req *connect.Request[reliantv1.RevertFilesRequest],
) (*connect.Response[reliantv1.RevertFilesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.WorktreeId == "" {
		return nil, svcerr.InvalidArgument("worktree_id is required")
	}

	if len(req.Msg.Files) == 0 {
		return nil, svcerr.InvalidArgument("at least one file is required")
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

	repoPath, _, err := s.resolveRepoPath(ctx, worktree, req.Msg.RepoId)
	if err != nil {
		return nil, err
	}

	// Route revert through daemon
	type revertResult struct {
		File    string `json:"file"`
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	var revertResp struct {
		Results []revertResult `json:"results"`
		Error   string         `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.revert", map[string]interface{}{
		"worktree_path": repoPath,
		"files":         req.Msg.Files,
	}, &revertResp); err != nil {
		return nil, daemonerr.Error("revert files", err)
	}

	if revertResp.Error != "" {
		// The daemon's own report of what git said.
		return nil, svcerr.Internal(revertResp.Error)
	}

	var revertedFiles []string
	var errors []string
	for _, r := range revertResp.Results {
		if r.Success {
			revertedFiles = append(revertedFiles, r.File)
		} else {
			errors = append(errors, fmt.Sprintf("%s: %s", r.File, r.Error))
		}
	}

	if len(revertedFiles) == 0 && len(errors) > 0 {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf("failed to revert requested files: %s", strings.Join(errors, "; ")))
	}

	message := fmt.Sprintf("Reverted %d file(s)", len(revertedFiles))
	if len(errors) > 0 {
		message = fmt.Sprintf("%s, %d error(s): %s", message, len(errors), strings.Join(errors, "; "))
	}

	return connect.NewResponse(&reliantv1.RevertFilesResponse{
		Message: message,
		Files:   revertedFiles,
	}), nil
}
