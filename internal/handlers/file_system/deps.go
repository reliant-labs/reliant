// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package file_system

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of *db.Repo the filesystem handlers use.
type Repository interface {
	// GetChat, GetWorktree and GetProject resolve a request's workspace
	// (filepreview.ResolveBasePath) and its machine (daemonplacement).
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	// ListProjectDaemonsForProject reads a missing checkout's clone records.
	ListProjectDaemonsForProject(ctx context.Context, projectID string) ([]*core.ProjectDaemon, error)
	// GetDaemon checks that a machine about to be woken is the caller's
	// (machinewake.Owners).
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
	// ListDaemonsByUserID and AdoptWorktreeDaemon settle the machine of a
	// workspace whose row records none (daemonplacement.Resolver).
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	AdoptWorktreeDaemon(ctx context.Context, id, daemonID string) error
}

// Router is the slice of toolexec.DaemonRouter the filesystem handlers use.
// A router that can also resume a suspended machine (machinewake.Resumer)
// has asleep machines woken; see wake.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	ResolveDaemonID(ctx context.Context, userID string) (string, error)
}
