package worktree

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
)

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
