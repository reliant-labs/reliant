// Copyright (c) 2025 Reliant Labs
package worktree

// This file contains WorktreeService git operations routed through daemon commands.
// These were extracted from worktree.go to route all filesystem/exec operations through the daemon.
//
// Each returns the error its rpc returns. A command that did not complete is
// classified by daemonerr (the machine's state picks the code, the daemon's own
// words are kept, the transport's withheld). A command the daemon ran and
// git refused is reported in git's words, which come from the user's own
// machine and say what to fix.

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/daemonerr"
)

// commitViaDaemon commits staged changes via daemon. daemonID is the
// worktree's owner (see daemonplacement.WorktreeOwner), as for every helper here.
func (s *Service) commitViaDaemon(ctx context.Context, userID, daemonID, worktreePath, message string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.commit", map[string]string{
		"worktree_path": worktreePath,
		"message":       message,
	}, &resp); err != nil {
		return "", daemonerr.Error("commit", err)
	}
	if !resp.Success {
		return resp.Output, svcerr.Internal("failed to commit: " + resp.Error)
	}
	return resp.Output, nil
}

// pushViaDaemon pushes the current branch (resolved by the daemon from HEAD).
// The service no longer passes a branch — the daemon's worktree.push reads
// HEAD on the resolved checkout dir, ensuring writes target whatever the user
// has actually checked out (which can diverge per-repo from worktree.Branch).
func (s *Service) pushViaDaemon(ctx context.Context, userID, daemonID, worktreePath string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.push", map[string]string{
		"worktree_path": worktreePath,
	}, &resp); err != nil {
		return "", daemonerr.Error("push", err)
	}
	if !resp.Success {
		return resp.Output, svcerr.Internal("failed to push: " + resp.Error)
	}
	return resp.Output, nil
}

// pullViaDaemon pulls the current branch (resolved by the daemon from HEAD).
// See pushViaDaemon for rationale.
func (s *Service) pullViaDaemon(ctx context.Context, userID, daemonID, worktreePath string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.pull", map[string]string{
		"worktree_path": worktreePath,
	}, &resp); err != nil {
		return "", daemonerr.Error("pull", err)
	}
	if !resp.Success {
		return resp.Output, svcerr.Internal("failed to pull: " + resp.Error)
	}
	return resp.Output, nil
}
