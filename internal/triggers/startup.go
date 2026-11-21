// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/retry"

	"github.com/reliant-labs/reliant/internal/logging"
)

// startupSyncAttempts bounds the retries. Every write path calls
// Sync for its own trigger, so a startup that never converges is degraded rather than
// broken: existing schedules keep firing, and the first edit to a trigger
// repairs it. Retrying forever would instead leave a goroutine hammering an
// unreachable Temporal for the life of the process.
const startupSyncAttempts = 6

// startupSyncBackoff waits 2s, 4s, 8s, 16s, 32s between the attempts.
var startupSyncBackoff = retry.Backoff{Initial: 2 * time.Second, Max: time.Minute}

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
	syncAllWithRetry(ctx, nil, syncer.SyncAll)
}

// syncAllWithRetry runs syncAll up to startupSyncAttempts times, pacing the
// retries on clock (nil is the system clock). A ctx that ends first stops it
// quietly; running out of attempts logs an error.
func syncAllWithRetry(ctx context.Context, clock retry.Clock, syncAll func(context.Context) error) {
	attempt := 0
	err := retry.Do(ctx, retry.Policy{
		Backoff:     startupSyncBackoff,
		MaxAttempts: startupSyncAttempts,
		Clock:       clock,
		Name:        "trigger schedule startup sync",
	}, func(actx context.Context) error {
		attempt = retry.AttemptNumber(actx)
		err := syncAll(ctx)
		if err != nil {
			logging.WarnContext(ctx, "trigger schedule sync failed",
				"attempt", attempt, "of", startupSyncAttempts, "error", err)
		}
		return err
	})
	if err == nil {
		logging.InfoContext(ctx, "trigger schedules converged", "attempt", attempt)
		return
	}
	if attempt < startupSyncAttempts {
		return // ctx ended before the attempts ran out
	}
	logging.ErrorContext(ctx, "trigger schedules did not converge at startup; "+
		"existing schedules keep firing and the next write or restart will repair them")
}
