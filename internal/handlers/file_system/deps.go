// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository;
// daemonplacement.Records satisfies Placement.
package file_system

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of *db.Repo the filesystem handlers use: the chat
// a request names. With Placement it resolves a request's workspace
// (filepreview.ResolveBasePath) and its machine (daemonplacement).
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
}

// Placement reads the workspace, project and machine rows a request is placed
// by (daemonplacement.Records in production). It checks no owner: every
// handler checks the returned row against the caller.
type Placement interface {
	// GetWorktree and GetProject resolve a request's workspace and check it
	// is the caller's; the main checkout is that machine's project_daemons
	// row (projectcheckout).
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	// ListProjectDaemonsForProject reads a missing checkout's clone records.
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	// GetDaemon checks that a machine about to be woken is the caller's
	// (machinewake.Owners).
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// Router is the slice of toolexec.DaemonRouter the filesystem handlers use.
// A router that can also resume a suspended machine (machinewake.Resumer)
// has asleep machines woken; see wake.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
}
