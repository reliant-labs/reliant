// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/permission"
)

// ProjectPermissionHelper provides permission checking for project-scoped resources
// Used by FileSystemHandler and other handlers that need to verify project ownership
type ProjectPermissionHelper struct {
	database db.Repository
}

// NewProjectPermissionHelper creates a new permission helper
func NewProjectPermissionHelper(database db.Repository) *ProjectPermissionHelper {
	return &ProjectPermissionHelper{
		database: database,
	}
}

// ProjectBelongsToUser checks if a project belongs to a user
func (h *ProjectPermissionHelper) ProjectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := h.database.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "access denied") {
			return permission.ErrResourceNotFound
		}
		return err
	}

	return nil
}

// Can checks if the current user can perform an action on a project
// All actions (view, update, delete) require ownership
func (h *ProjectPermissionHelper) Can(r *http.Request, action permission.Action, projectID string) error {
	ctx := r.Context()
	userID := auth.MustGetUserID(ctx)

	return h.ProjectBelongsToUser(ctx, projectID, userID)
}
