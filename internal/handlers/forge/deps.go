// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package forge

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Router is the slice of toolexec.DaemonRouter the forge handlers use
// (daemoncmd.Sender).
type Router interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Repository is the only database capability this service needs: resolve a
// project id to its row, enforcing that the caller owns it, and record forge's
// name for the project when a topology report carries one.
//
// Two methods are all this service uses, and a narrow dependency is what makes
// the handler testable without a database — which is what lets the
// secret-safety and reachability tests run as plain unit tests.
// *db.Repo satisfies it.
type Repository interface {
	GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*db.Project, error)
	SetProjectForgeName(ctx context.Context, id, userID, forgeProjectName string) (bool, error)
}
