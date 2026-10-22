// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package repo

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AdoptStore is the repos registry AdoptFromDaemon reconciles with the
// filesystem, plus the project row whose IsGitRepo it trues up.
type AdoptStore interface {
	GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error)
	CreateRepo(ctx context.Context, repo *core.Repo) error
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	UpdateProject(ctx context.Context, project *db.Project, userID string) error
}
