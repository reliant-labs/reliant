// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// startupSyncAttempts bounds the retries. SyncAll is also called by every
// write path, so a startup that never converges is degraded rather than
// broken: existing schedules keep firing, and the first edit to a trigger
// repairs it. Retrying forever would instead leave a goroutine hammering an
// unreachable Temporal for the life of the process.
const startupSyncAttempts = 6

// SyncAllOnStartup converges every trigger's schedule in the background,
// retrying with backoff.
//
// This runs in a goroutine and never blocks startup. The api-server must come
// up and serve requests whether or not Temporal is reachable yet — in a
// cold cluster start they race, and refusing to serve because schedules have
// not converged would turn a transient ordering problem into an outage.
func SyncAllOnStartup(ctx context.Context, syncer *Syncer) {
	if syncer == nil {
		return
	}
	backoff := 2 * time.Second
	for attempt := 1; attempt <= startupSyncAttempts; attempt++ {
		err := syncer.SyncAll(ctx)
		if err == nil {
			logging.Info("trigger schedules converged", "attempt", attempt)
			return
		}
		logging.Warn("trigger schedule sync failed",
			"attempt", attempt, "of", startupSyncAttempts, "error", err)

		if attempt == startupSyncAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
	logging.Error("trigger schedules did not converge at startup; " +
		"existing schedules keep firing and the next write or restart will repair them")
}
