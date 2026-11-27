// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them.
package coverage_source

import (
	"context"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// Projects is the slice of the repository the GitHub remote is found
// through. *db.Repo satisfies it.
type Projects interface {
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	ListReposHoldingForgeApp(ctx context.Context, name string, projectIDs []string) ([]*core.Repo, error)
}

// OrgResolver answers which org a read acts in. *orgs.Resolver is one.
type OrgResolver interface {
	DefaultOrg(ctx context.Context, userID string) (string, error)
	ListScope(ctx context.Context, userID, requested string, stamp orgs.Backfill) (*core.OrgScope, error)
}

// Reports asks control-plane, as the viewer, whether a retained coverage
// report of a forge project names a blob. controlplane.Service is one.
type Reports interface {
	HasCoverageBlob(ctx context.Context, token, orgID, project, blobID string) (controlplane.CoverageBlob, error)
}

// Blobs reads a git blob from GitHub as the viewer. ghblobs.Service is one.
type Blobs interface {
	Read(ctx context.Context, userID, owner, repo, blobID string) ([]byte, error)
}
