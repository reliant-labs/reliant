// Copyright (c) 2025 Reliant Labs

// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemonstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DerivationRepository is the narrow persistence surface the Derivation
// consumer needs. Satisfied by *db.Repo.
type DerivationRepository interface {
	UpsertDaemonAttachment(ctx context.Context, att *db.DaemonAttachment) error
	TouchDaemonAttachmentIfNewer(ctx context.Context, daemonID string, activityAt time.Time) error
	DeleteDaemonAttachment(ctx context.Context, daemonID string) error
	DeleteStaleDaemonAttachments(ctx context.Context, olderThan time.Duration) (int64, error)
}

const (
	// AttachmentTTL is how long an unrenewed attachment row survives before
	// the reaper deletes it.
	//
	// The row is a LEASE, not a record: a live stream renews it on every
	// inbound heartbeat (15s), and every reader already treats it as dead
	// past 90s (daemonAttachmentStaleThreshold) or 2m (/flow-health). A row
	// untouched for 15m is therefore useless to every reader and cannot
	// belong to a live stream on any replica — 10x the widest reader window
	// is margin enough for a NATS hiccup or a paused DB.
	//
	// Deletion is the ONLY way these rows ever leave: the disconnect path
	// (teardownConnection → DeleteDaemonAttachment) requires the owning
	// process to still be alive to run it, so a crashed, rescheduled, or
	// redeployed gateway strands its rows permanently. Dev's registry on
	// 2026-08-24 held two such orphans — one 29 days old, one for a
	// workspace pod deleted 51 days earlier — and they had pinned
	// /flow-health at 503 for the whole environment ever since.
	AttachmentTTL = 15 * time.Minute

	// attachmentReapInterval is how often the reaper runs. Cheap (one
	// indexed DELETE on idx_daemon_attachment_last_activity) and not
	// urgent: nothing reads a row this stale.
	attachmentReapInterval = 5 * time.Minute
)

// Derivation is the reliant-side consumer of the daemon.v1.state.> subject.
// It mirrors lifecycle events into the daemon_attachment table so the
// existing readers (IsDaemonAttached, ListAttachedDaemonIDsForUser) see the
// gateway's view without each writer touching the table directly.
//
// Plain NATS, not JetStream: liveness is "newest wins". A lost event is
// re-supplied by the next activity tick from the gateway publisher; durable
// replay of stale state on restart is worse than starting cold.
type Derivation struct {
	nc   *nats.Conn
	repo DerivationRepository
}

// NewDerivation constructs the consumer.
func NewDerivation(nc *nats.Conn, repo DerivationRepository) *Derivation {
	return &Derivation{nc: nc, repo: repo}
}

// Start subscribes to SubjectWildcard and blocks until ctx is cancelled.
// Returns nil on clean shutdown.
func (d *Derivation) Start(ctx context.Context) error {
	if d.nc == nil {
		return errors.New("daemonstate: nil NATS connection")
	}
	if d.repo == nil {
		return errors.New("daemonstate: nil repository")
	}

	sub, err := d.nc.Subscribe(SubjectWildcard, func(msg *nats.Msg) {
		// Detach from the NATS dispatch goroutine so a slow DB call doesn't
		// stall the subscription. NATS-side delivery has no backpressure here;
		// the gateway publisher is rate-limited.
		go d.handle(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("daemonstate: subscribe %s: %w", SubjectWildcard, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// The reaper rides along with the consumer because this type is the
	// single writer to daemon_attachment: expiring a lease is a write, and
	// keeping every write in one place is what makes the table's contents
	// explainable. It is NOT the gateway's stale-connection sweeper — that
	// one reaps entries in ONE process's connection map and deletes their
	// rows as a side effect, which by construction can never touch a row
	// whose process is gone.
	go d.reapLoop(ctx)

	logging.Info(logPrefix+" derivation consumer started", "subject", SubjectWildcard)
	<-ctx.Done()
	logging.Info(logPrefix + " derivation consumer shutting down")
	return nil
}

// reapLoop deletes expired attachment leases on a ticker until ctx is done.
//
// It sweeps once immediately: a gateway that just started is the most likely
// moment for orphans to exist (its predecessor's rows, if it died rather than
// shutting down), and there is no reason to make an operator wait a full
// interval to see the registry tell the truth. Idempotent and racy-safe, so
// every replica running it concurrently is fine.
func (d *Derivation) reapLoop(ctx context.Context) {
	ticker := time.NewTicker(attachmentReapInterval)
	defer ticker.Stop()

	d.reapOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.reapOnce(ctx)
		}
	}
}

// reapOnce runs one TTL sweep. Failures are logged, never fatal — a GC that
// cannot run must not take the consumer (or the gateway) down with it.
func (d *Derivation) reapOnce(ctx context.Context) {
	n, err := d.repo.DeleteStaleDaemonAttachments(ctx, AttachmentTTL)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down
		}
		logging.Warn(logPrefix+" attachment reap failed", "ttl", AttachmentTTL, "error", err)
		return
	}
	if n > 0 {
		logging.Info(logPrefix+" reaped expired daemon attachments",
			"count", n, "ttl", AttachmentTTL)
	}
}

func (d *Derivation) handle(ctx context.Context, msg *nats.Msg) {
	var evt Event
	if err := json.Unmarshal(msg.Data, &evt); err != nil {
		logging.Warn(logPrefix+" malformed event, dropping", "subject", msg.Subject, "error", err)
		return
	}
	if evt.DaemonID == "" || evt.At.IsZero() {
		logging.Warn(logPrefix+" event missing required fields, dropping", "subject", msg.Subject)
		return
	}
	if err := d.dispatch(ctx, evt); err != nil {
		// Plain NATS gives us no redelivery — log and move on. The next
		// gateway tick re-supplies truth.
		logging.Warn(logPrefix+" event processing failed",
			"daemonID", evt.DaemonID, "type", evt.Type, "error", err)
	}
}

// dispatch routes one event to the appropriate handler. Idempotent.
func (d *Derivation) dispatch(ctx context.Context, evt Event) error {
	switch evt.Type {
	case EventConnected:
		return d.onConnected(ctx, evt)
	case EventActivity:
		return d.onActivity(ctx, evt)
	case EventDisconnected:
		return d.onDisconnected(ctx, evt)
	default:
		return fmt.Errorf("unknown event type %q", evt.Type)
	}
}

func (d *Derivation) onConnected(ctx context.Context, evt Event) error {
	att := &db.DaemonAttachment{
		DaemonID:           evt.DaemonID,
		UserID:             evt.UserID,
		Source:             db.DaemonAttachmentSourceInbound,
		AttachedAt:         evt.At,
		LastStreamActivity: evt.At,
	}
	if err := d.repo.UpsertDaemonAttachment(ctx, att); err != nil {
		return fmt.Errorf("upsert attachment %s: %w", evt.DaemonID, err)
	}
	return nil
}

func (d *Derivation) onActivity(ctx context.Context, evt Event) error {
	// No-op when the row doesn't exist (activity racing ahead of connected)
	// or when the stored timestamp is already newer (out-of-order NATS
	// delivery). The next connected event creates the row; the next in-order
	// activity catches us up.
	if err := d.repo.TouchDaemonAttachmentIfNewer(ctx, evt.DaemonID, evt.At); err != nil {
		return fmt.Errorf("touch attachment %s: %w", evt.DaemonID, err)
	}
	return nil
}

func (d *Derivation) onDisconnected(ctx context.Context, evt Event) error {
	if err := d.repo.DeleteDaemonAttachment(ctx, evt.DaemonID); err != nil {
		return fmt.Errorf("delete attachment %s: %w", evt.DaemonID, err)
	}
	return nil
}
