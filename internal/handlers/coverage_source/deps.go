// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them.
package coverage_source

import (
	"context"
	"errors"

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

// Blobs reads a git blob as the viewer. No server role implements it: a
// server holds no repository bytes (owner, 2026-11-27; internal/architecture's
// TestServerRolesRunNoCode), so until a daemon-side reader exists every read
// is NOT_YET_AVAILABLE. An implementation answers the Err* errors below.
type Blobs interface {
	Read(ctx context.Context, userID, owner, repo, blobID string) ([]byte, error)
}

var (
	// ErrTokenRejected is the viewer's token refused: they must reconnect.
	ErrTokenRejected = errors.New("coverage source: the token was rejected")
	// ErrNotFound is no such blob, or a repository the viewer cannot see.
	ErrNotFound = errors.New("coverage source: no such blob for this viewer")
	// ErrTooLarge is a blob over the size a coverage page shows.
	ErrTooLarge = errors.New("coverage source: the blob is larger than a coverage page shows")
	// ErrInvalid is an owner, repository or blob id nothing could name.
	ErrInvalid = errors.New("coverage source: not a repository and blob id")
)
