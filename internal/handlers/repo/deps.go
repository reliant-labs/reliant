// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package repo

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of db.Repository the repo handlers use.
type Repository interface {
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	UpdateProject(ctx context.Context, project *db.Project, userID string) error

	GetRepo(ctx context.Context, id string) (*core.Repo, error)
	GetRepoByProjectAndPath(ctx context.Context, projectID, relativePath string) (*core.Repo, error)
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	CreateRepo(ctx context.Context, repo *core.Repo) error
	DeleteRepo(ctx context.Context, id string) error
}

// Sender sends one request/response command to a user's daemon.
// toolexec.DaemonRouter implements it.
type Sender interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}
