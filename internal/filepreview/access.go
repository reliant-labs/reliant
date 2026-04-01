package filepreview

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
)

var ErrPathOutsideBase = errors.New("requested path is outside base directory")

// ResolveBasePath resolves the correct base path for file operations.
// Returns the worktree path if worktree_id or chat_id is provided, otherwise returns project path.
func ResolveBasePath(ctx context.Context, repo db.Repository, projectID string, worktreeID *string, chatID *string) (string, error) {
	wtID := ""
	if worktreeID != nil {
		wtID = *worktreeID
	}

	if wtID == "" && chatID != nil && *chatID != "" {
		chat, err := repo.GetChat(ctx, *chatID)
		if err == nil && chat.WorktreeID != nil && *chat.WorktreeID != "" {
			wtID = *chat.WorktreeID
		}
	}

	if wtID != "" {
		worktree, err := repo.GetWorktree(ctx, wtID)
		if err == nil {
			return worktree.Path, nil
		}
	}

	project, err := repo.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	return project.Path, nil
}

// ValidatePath performs security checks to ensure path is within the base directory.
func ValidatePath(basePath, requestedPath string) (string, error) {
	fullPath := filepath.Join(basePath, requestedPath)

	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}
	absBasePath, err := filepath.Abs(basePath)
	if err != nil {
		return "", err
	}

	if absFullPath != absBasePath && !strings.HasPrefix(absFullPath, absBasePath+string(filepath.Separator)) {
		return "", ErrPathOutsideBase
	}

	return absFullPath, nil
}
