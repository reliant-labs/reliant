// Copyright (c) 2025 Reliant Labs

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
}

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

	logging.Info(logPrefix+" derivation consumer started", "subject", SubjectWildcard)
	<-ctx.Done()
	logging.Info(logPrefix + " derivation consumer shutting down")
	return nil
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
