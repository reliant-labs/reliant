// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package mcp

import (
	"context"

	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ProjectReader reads a project by id. It checks no owner: every caller
// decides access on the row it returns. store.ProjectExtStore satisfies it.
type ProjectReader interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// Repository is the slice of *db.Repo the mcp handlers use.
type Repository interface {
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)

	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error

	// Installs is each machine's checkout of the project and who owns each
	// machine (projectcheckout.Resolve).
	daemonplacement.Installs
}

// ProjectConfigs is the stored project config record the MCP configuration
// is kept in step with. store.ProjectConfigExtStore satisfies it.
type ProjectConfigs interface {
	GetProjectConfigRecord(ctx context.Context, projectID string) (*core.ProjectConfigRecord, error)
	GetProjectMCPConfigsJSON(ctx context.Context, projectID string) (*string, error)
	UpsertProjectConfigRecord(ctx context.Context, rec *core.ProjectConfigRecord) error
}

// Router sends commands to the machine holding a project's checkout.
// *toolexec.NATSDaemonRouter implements it.
type Router interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// PlacementDaemonID ranks the machines holding a checkout the way the
	// project's other work is placed (daemonplacement.Machines).
	PlacementDaemonID(ctx context.Context, userID, daemonID string) (string, error)
}
