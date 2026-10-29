// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from the database; mock_gen.go mocks them. *db.Repo satisfies them.
package toolexec

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
)

// DaemonRecords is the daemon registry the NATS router reads for resolution
// and liveness: the user's daemon rows, their attachment leases and when a
// daemon was last seen (WithDatabase, RouterConfig.DB).
type DaemonRecords interface {
	ListDaemonsByUserID(ctx context.Context, userID string) ([]*db.Daemon, error)
	ListAttachedDaemonIDsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]string, error)
	IsDaemonAttached(ctx context.Context, userID string, staleThreshold time.Duration) (bool, error)
	IsDaemonIDAttached(ctx context.Context, daemonID string, staleThreshold time.Duration) (bool, error)
	DaemonLastSeenAt(ctx context.Context, userID, daemonID string) (time.Time, error)
	ListFreshDaemonAttachmentsForUser(ctx context.Context, userID string, staleThreshold time.Duration) ([]*db.DaemonAttachment, error)
}
