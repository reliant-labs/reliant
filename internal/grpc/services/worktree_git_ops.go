// Copyright (c) 2025 Reliant Labs
package services

// This file contains WorktreeService git operations routed through daemon commands.
// These were extracted from worktree.go to route all filesystem/exec operations through the daemon.

import (
	"context"
	"fmt"
)

// commitViaCmd commits staged changes via daemon
func (s *WorktreeService) commitViaDaemon(ctx context.Context, userID, worktreePath, message string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.commit", map[string]string{
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

func (s *WorktreeService) pushViaDaemon(ctx context.Context, userID, worktreePath, branch string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.push", map[string]string{
		"worktree_path": worktreePath,
		"branch":        branch,
	}, &resp); err != nil {
		return "", fmt.Errorf("daemon push: %w", err)
	}
	if !resp.Success {
		return resp.Output, fmt.Errorf("%s", resp.Error)
	}
	return resp.Output, nil
}

func (s *WorktreeService) pullViaDaemon(ctx context.Context, userID, worktreePath, branch string) (string, error) {
	var resp struct {
		Success bool   `json:"success"`
		Output  string `json:"output,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.pull", map[string]string{
		"worktree_path": worktreePath,
		"branch":        branch,
	}, &resp); err != nil {
		return "", fmt.Errorf("daemon pull: %w", err)
	}
	if !resp.Success {
		return resp.Output, fmt.Errorf("%s", resp.Error)
	}
	return resp.Output, nil
}

func (s *WorktreeService) getDefaultBranchViaDaemon(ctx context.Context, userID, repoPath string) string {
	var resp struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := s.sendWorktreeDaemonCommand(ctx, userID, "worktree.get_default_branch", map[string]string{
		"repo_path": repoPath,
	}, &resp); err != nil {
		return "main"
	}
	if resp.DefaultBranch == "" {
		return "main"
	}
	return resp.DefaultBranch
}
