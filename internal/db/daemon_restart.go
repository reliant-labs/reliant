package db

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/store"
)

// CompareDaemonRestartStatus restores/clears only the exact intent read by the
// current attachment, excluding both old sessions and concurrent new intents.
func (r *Repo) CompareDaemonRestartStatus(ctx context.Context, userID, daemonID string, attachedAt time.Time, previous, status string) error {
	return store.CompareDaemonRestartStatus(ctx, r.ORM(), userID, daemonID, attachedAt, previous, status)
}

// SetDaemonRestartStatus records the owning stream's planned drain, including
// clearing it when a replacement process registers.
func (r *Repo) SetDaemonRestartStatus(ctx context.Context, userID, daemonID string, attachedAt time.Time, status string) error {
	return store.SetDaemonRestartStatus(ctx, r.ORM(), userID, daemonID, attachedAt, status)
}
