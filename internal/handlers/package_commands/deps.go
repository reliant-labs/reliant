// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository;
// daemonplacement.Records satisfies Placement.
package package_commands

import (
	"context"

	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
)

// Router is the slice of toolexec.DaemonRouter the rpcs use: discovery and
// every package-command process go to the machine holding the workspace. A
// router that also reports liveness (IsDaemonOnline) has lists skip asleep
// machines instead of waking them.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// SendDaemonCommandToDaemon reaches the machine holding a workspace's
	// checkout, which is not always the default one.
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Repository is the slice of *db.Repo the package-commands handlers use.
type Repository interface {
	// ListDaemonsByUserID lists the user's machines for ListProcesses.
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
}

// Favorites keeps a user's favorite commands of a project.
// store.CommandFavoriteExtStore satisfies it.
type Favorites interface {
	ListCommandFavorites(ctx context.Context, userID, projectID string) ([]string, error)
	AddCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
	RemoveCommandFavorite(ctx context.Context, userID, projectID, commandKey string) error
}

// Placement reads the workspace, project and machine rows a command is placed
// by (daemonplacement.Records in production). It checks no owner: the
// handlers check the returned rows against the caller.
type Placement interface {
	// GetChat completes daemonplacement.WorkspaceReader.
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetWorktree(ctx context.Context, id string) (*db.Worktree, error)
	GetProject(ctx context.Context, id string) (*db.Project, error)
	// Installs places the main checkout's commands where the project is
	// installed (daemonplacement.ProjectDefaultDaemonID) and checks a machine
	// a request names (GetDaemon).
	daemonplacement.Installs
}
