// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// checkWorkspaceMachine refuses running a chat in worktreeID on daemonID when
// the worktree's checkout lives on another machine. A branch workspace exists
// on one machine's disk only: a chat pinned elsewhere has tools, Files and a
// terminal pointed at a directory that is not there ("Its workspace was
// archived or removed from the machine").
//
// Refused rather than silently moved to the owner: the caller named a machine,
// and quietly running somewhere else is a surprise the user cannot see. The
// new-chat composer never sends the combination — selecting a workspace
// selects its machine — so this is the server's half of that rule. The error
// names the machine to pick.
//
// An empty worktreeID or daemonID, the main checkout, a row that records no
// machine (made before owners were recorded), and a worktree that no longer
// exists never conflict.
func (s *Service) checkWorkspaceMachine(ctx context.Context, userID, worktreeID, daemonID string) error {
	if worktreeID == "" {
		return nil
	}
	worktree, err := s.deps.Repo.GetWorktree(ctx, worktreeID)
	if errors.Is(err, core.ErrWorktreeNotFound) {
		return nil
	}
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to load the workspace"))
	}
	// Another user's workspace is not found, before its machine is looked up:
	// the conflict message names that machine, so checking ownership after
	// would hand its hostname to whoever guessed the worktree id.
	project, err := s.deps.Repo.GetProject(ctx, worktree.ProjectID)
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return svcerr.WrapOr(err, svcerr.Internal("failed to load the workspace"))
	}
	if project == nil || project.UserID != userID {
		return svcerr.NotFound("worktree")
	}
	owner := daemonplacement.WorktreeOwner(worktree)
	if daemonID == "" || owner == "" || owner == daemonID {
		return nil
	}
	return svcerr.FailedPrecondition(daemonplacement.WorkspaceElsewhereMessage(ctx, s.deps.Repo, owner))
}
