// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package shell

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// ProcessStore is the background_processes rows the BackgroundManager reads
// and writes to control a process it recovered after a restart (one it holds
// no handle for), set with SetRepository.
type ProcessStore interface {
	GetBackgroundProcess(ctx context.Context, id string) (*db.BackgroundProcess, error)
	UpdateBackgroundProcessStatus(ctx context.Context, id string, status db.BackgroundProcessStatus, exitCode *int, endedAt *time.Time) error
}
