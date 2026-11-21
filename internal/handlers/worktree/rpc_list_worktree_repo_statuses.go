package worktree

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

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

	var statuses []*reliantv1.WorktreeRepoStatus
	err = s.gitReadOnDisk(ctx, userID, worktree, func() error {
		repos, err := s.deps.Repo.ListReposByProject(ctx, worktree.ProjectID)
		if err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to list repos for project"))
		}
		// 0-repo project: empty response. The UI treats this as "nothing to
		// show in the sidebar" rather than an error so newly-created projects
		// without any nested repo registered yet still render cleanly.
		statuses = make([]*reliantv1.WorktreeRepoStatus, len(repos))
		if len(repos) == 0 {
			return errDirUnverified
		}
		// The repos are independent reads of one machine: asked at once, the
		// answer takes the slowest repo's time, not the sum.
		var wg sync.WaitGroup
		for i, r := range repos {
			wg.Add(1)
			go func() {
				defer wg.Done()
				statuses[i] = s.repoStatus(ctx, userID, worktree, r)
			}()
		}
		wg.Wait()
		for _, row := range statuses {
			if row.Error != "" {
				return errDirUnverified
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errDirUnverified) {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.ListWorktreeRepoStatusesResponse{
		Statuses: statuses,
	}), nil
}

// repoStatus is one ListWorktreeRepoStatuses row: r's git status in worktree,
// or the failure to read it.
func (s *Service) repoStatus(ctx context.Context, userID string, worktree *db.Worktree, r *core.Repo) *reliantv1.WorktreeRepoStatus {
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
	if err := s.sendWorktreeDaemonCommandTimeout(ctx, userID, daemonplacement.WorktreeOwner(worktree), "worktree.git_status", map[string]string{
		"worktree_path": filepath.Join(worktree.Path, r.RelativePath),
		"branch":        worktree.Branch,
	}, &statusResp, worktreeReadCommandTimeoutMs); err != nil {
		row.Error = err.Error()
		return row
	}
	row.CurrentBranch = statusResp.Branch
	row.HasChanges = statusResp.HasChanges
	row.Ahead = statusResp.Ahead
	row.Behind = statusResp.Behind
	row.ChangedFiles = int32(len(statusResp.StagedFiles) + len(statusResp.UnstagedFiles) + len(statusResp.UntrackedFiles))
	if statusResp.Error != "" {
		row.Error = statusResp.Error
	}
	return row
}
