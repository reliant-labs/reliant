// Copyright (c) 2025 Reliant Labs
package serverapi

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// backgroundProcessLeaseWindow is how fresh a daemon_attachment lease must be
// for the daemon to count as connected. Six missed 15s heartbeats, the same
// window the daemon router uses to decide a daemon is routable.
const backgroundProcessLeaseWindow = 90 * time.Second

// worktreeSweepInterval is how often the server asks each daemon to settle its
// archived worktrees. Archiving already attempts removal immediately, so this
// only catches worktrees the daemon was offline or busy for.
const worktreeSweepInterval = 10 * time.Minute

// backgroundProcessDaemons adapts the daemon router and the attachment table
// to reconciliation.BackgroundProcessDaemons: the reconciler asks a daemon
// about its processes through the router, and learns which daemons are
// connected from the attachment leases the gateway renews on every heartbeat.
type backgroundProcessDaemons struct {
	router toolexec.DaemonRouter
	repo   db.Repository
}

func (d backgroundProcessDaemons) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return d.router.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
}

func (d backgroundProcessDaemons) ConnectedDaemonIDs(ctx context.Context, userID string) ([]string, error) {
	return d.repo.ListAttachedDaemonIDsForUser(ctx, userID, backgroundProcessLeaseWindow)
}
