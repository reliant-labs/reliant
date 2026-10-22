// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package mcp

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the mcp handlers use.
type Repository interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)

	GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error)
	GetProjectMCPConfigsJSON(ctx context.Context, projectID string) (*string, error)
	UpsertProjectConfigRecord(ctx context.Context, record *db.ProjectConfigRecord) error

	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
}

// Router sends one request/response command to a user's daemon.
// toolexec.DaemonRouter implements it.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}
