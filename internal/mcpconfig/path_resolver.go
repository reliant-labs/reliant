package mcpconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
)

// ResolveProjectForMCPPath resolves a canonical project identity for an MCP scope path.
//
// Resolution order is deterministic and DB-backed only:
//  1. Exact project path match
//  2. Exact worktree path match -> owning project
func ResolveProjectForMCPPath(ctx context.Context, repo db.Repository, projectPath string) (*db.Project, error) {
	path := strings.TrimSpace(projectPath)
	if path == "" {
		return nil, fmt.Errorf("project path is required for MCP config resolution")
	}

	project, projectErr := repo.GetProjectByPath(ctx, path)
	if projectErr == nil && project != nil {
		return project, nil
	}
	if projectErr != nil && !isNotFoundErr(projectErr) {
		return nil, fmt.Errorf("failed to resolve project by path for MCP config: %w", projectErr)
	}

	worktree, worktreeErr := repo.GetWorktreeByPath(ctx, path)
	if worktreeErr != nil {
		if isNotFoundErr(worktreeErr) {
			return nil, fmt.Errorf("project or worktree not found for MCP config path: %s", path)
		}
		return nil, fmt.Errorf("failed to resolve worktree by path for MCP config: %w", worktreeErr)
	}
	if worktree == nil || strings.TrimSpace(worktree.ProjectID) == "" {
		return nil, fmt.Errorf("worktree project mapping missing for MCP config path: %s", path)
	}

	project, err := repo.GetProject(ctx, worktree.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve owning project for worktree path %s: %w", path, err)
	}
	if project == nil {
		return nil, fmt.Errorf("owning project not found for worktree path: %s", path)
	}

	return project, nil
}

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}
