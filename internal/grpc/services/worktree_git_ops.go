// Copyright (c) 2025 Reliant Labs
package services

// This file contains WorktreeService git operations routed through daemon commands.
// These were extracted from worktree.go to route all filesystem/exec operations through the daemon.

import (
	"context"
	"fmt"
)

// commitViaDaemon commits staged changes via daemon. daemonID is the
// worktree's owner (see worktreeOwner), as for every helper here.
func (s *WorktreeService) commitViaDaemon(ctx context.Context, userID, daemonID, worktreePath, message string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.commit", map[string]string{
		"worktree_path": worktreePath,
		"message":       message,
	}, &resp); err != nil {
		return "", fmt.Errorf("daemon commit: %w", err)
	}
	if !resp.Success {
		return resp.Output, fmt.Errorf("%s", resp.Error)
	}
	return resp.Output, nil
}

// pushViaDaemon pushes the current branch (resolved by the daemon from HEAD).
// The service no longer passes a branch — the daemon's worktree.push reads
// HEAD on the resolved checkout dir, ensuring writes target whatever the user
// has actually checked out (which can diverge per-repo from worktree.Branch).
func (s *WorktreeService) pushViaDaemon(ctx context.Context, userID, daemonID, worktreePath string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.push", map[string]string{
		"worktree_path": worktreePath,
	}, &resp); err != nil {
		return "", fmt.Errorf("daemon push: %w", err)
	}
	if !resp.Success {
		return resp.Output, fmt.Errorf("%s", resp.Error)
	}
	return resp.Output, nil
}

// pullViaDaemon pulls the current branch (resolved by the daemon from HEAD).
// See pushViaDaemon for rationale.
func (s *WorktreeService) pullViaDaemon(ctx context.Context, userID, daemonID, worktreePath string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "worktree.pull", map[string]string{
		"worktree_path": worktreePath,
	}, &resp); err != nil {
		return "", fmt.Errorf("daemon pull: %w", err)
	}
	if !resp.Success {
		return resp.Output, fmt.Errorf("%s", resp.Error)
	}
	return resp.Output, nil
}
