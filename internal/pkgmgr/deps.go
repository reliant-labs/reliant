// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package pkgmgr

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// ProcessStore is the background_processes rows RecoveryService validates on
// startup: the ones marked running, and marking the dead ones stale.
type ProcessStore interface {
	GetRunningBackgroundProcesses(ctx context.Context) ([]*db.BackgroundProcess, error)
	MarkStaleProcesses(ctx context.Context, processIDs []string) error
}
