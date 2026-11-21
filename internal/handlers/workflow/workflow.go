// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// projectBelongsToUser verifies the authenticated user owns the given project,
// returning a Connect error if not (or if the project doesn't exist).
func (s *Service) projectBelongsToUser(ctx context.Context, projectID string, userID string) error {
	_, err := s.ownedProject(ctx, projectID, userID)
	return err
}

// ownedProject is projectBelongsToUser returning the project it read.
func (s *Service) ownedProject(ctx context.Context, projectID string, userID string) (*db.Project, error) {
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, projectID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}
	if project == nil {
		return nil, svcerr.NotFound("project")
	}
	return project, nil
}

// generateSlug is the slug a workflow name is stored and compared under —
// workflowref.Slug, the one slug every surface uses.
func generateSlug(name string) string {
	return workflowref.Slug(name)
}
