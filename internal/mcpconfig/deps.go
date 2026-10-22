// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package mcpconfig

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// PathStore is what ResolveProjectForMCPPath reads to map an MCP scope path
// to its project: the project or worktree at that path, and a worktree's
// owning project.
type PathStore interface {
	GetProjectByPath(ctx context.Context, path string) (*db.Project, error)
	GetWorktreeByPath(ctx context.Context, path string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
}
