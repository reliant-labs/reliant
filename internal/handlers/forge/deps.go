// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package forge

import (
	"context"

	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
)

// Router is the slice of toolexec.DaemonRouter the forge handlers use.
//
// Every forge.* command is PINNED to the machine holding the project's
// checkout (daemonplacement.ProjectCheckout), never sent by project-blind
// default resolution: the path it carries is that machine's path, and on any
// other machine it names nothing. *toolexec.NATSDaemonRouter satisfies it.
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// SendDaemonCommandToDaemon sends to the machine holding the checkout.
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	// PlacementDaemonID ranks the machines holding a checkout the way the
	// project's other work is placed (daemonplacement.Machines).
	PlacementDaemonID(ctx context.Context, userID, daemonID string) (string, error)
}

// Repository is the database capability this service needs: resolve a
// project id to its row, enforcing that the caller owns it; find the
// project's checkout on each of the caller's machines, and the caller's other
// projects declaring the same forge project; and record forge's name for the
// project when a topology report carries one.
//
// A narrow dependency is what makes the handler testable without a database —
// which is what lets the secret-safety and reachability tests run as plain
// unit tests. *db.Repo satisfies it.
type Repository interface {
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
	// ListProjects lists the caller's projects: a read whose machine is not
	// reachable is asked of another checkout of the same forge project
	// (forgeFallbackTargets), which may belong to another of them.
	ListProjects(ctx context.Context, filters db.ProjectFilters) ([]*db.Project, error)
	// Installs is each machine's checkout of the project (project_daemons)
	// and who owns each machine (daemonplacement.ProjectCheckout).
	daemonplacement.Installs
}
