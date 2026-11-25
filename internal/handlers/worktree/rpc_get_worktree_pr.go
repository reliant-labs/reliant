package worktree

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/logging"
)

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

	worktree, err := s.getWorktree(ctx, req.Msg.WorktreeId)
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
		Error      string `json:"error,omitempty"`
	}
	// Branch is resolved daemon-side from HEAD, not from worktree.Branch.
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.get_pr", map[string]string{
		"worktree_path": repoPath,
	}, &prResp); err != nil {
		logging.WarnContext(ctx, "Failed to check PR via daemon", "error", err)
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("could not check pull request; reconnect the machine and retry"))
	}
	if prResp.Error != "" {
		return nil, svcerr.Unavailable(prResp.Error)
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
