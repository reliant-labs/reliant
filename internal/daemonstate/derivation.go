// Copyright (c) 2025 Reliant Labs

// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the package IS the NATS liveness publisher; adapter conversion deferred (reliant is not forge-generated); tracked in H-RELIANT-CI-lint follow-ups
//forge:exclude-contract: wire contract + gateway publisher for the daemon liveness event stream
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
	ApplyDaemonLifecycle(ctx context.Context, lc db.DaemonLifecycleUpdate) (bool, error)
	UpsertDaemonIdentity(ctx context.Context, id db.DaemonIdentity) (bool, error)
	RemoveDaemon(ctx context.Context, daemonID string) (userID string, removed bool, err error)
	ApplyDaemonRegistrySnapshot(ctx context.Context, snap db.RegistrySnapshotApply) (db.RegistrySnapshotResult, error)
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

	// registrySnapshotGrace is how long a row this registry created is
	// protected from a snapshot that does not list it. The control plane
	// learns of a self-hosted daemon only after the gateway registered it
	// here (the connected event travels gateway → control plane), so a
	// snapshot read in that gap is stale about exactly that daemon. Ten
	// minutes is several snapshot periods: a daemon the control plane really
	// does not know is still removed, one sweep later.
	registrySnapshotGrace = 10 * time.Minute

	// registryAttachmentFreshness is how recent an attachment lease must be
	// for a row to count as connected and therefore real. Matches the
	// registry's own attachment staleness threshold
	// (services.daemonAttachmentStaleThreshold): a row the list reports as
	// connected must not be the row a snapshot deletes.
	registryAttachmentFreshness = 90 * time.Second

	// registrySnapshotQueue makes every snapshot apply on exactly one
	// replica. Unlike the per-daemon events — which every replica applies
	// and the SQL guards make idempotent — a snapshot is a whole owner's set
	// in one transaction, and N replicas racing it buys nothing but lock
	// contention.
	registrySnapshotQueue = "reliant-daemon-registry"
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
	nc     *nats.Conn
	repo   DerivationRepository
	notify LifecycleNotifier
}

// LifecycleNotifier is told when a lifecycle event changed a daemon's registry
// row, so the owner's web clients can refetch the daemon list instead of
// polling it. userID is whatever the event carried and may be empty — the
// control-plane's string-typed publisher does not know the owner — so the
// implementation resolves it.
type LifecycleNotifier func(ctx context.Context, userID, daemonID string)

// NewDerivation constructs the consumer.
func NewDerivation(nc *nats.Conn, repo DerivationRepository) *Derivation {
	return &Derivation{nc: nc, repo: repo}
}

// NotifyLifecycleApplied registers the callback onLifecycle runs after a
// lifecycle event actually changed a row. Call before Start.
func (d *Derivation) NotifyLifecycleApplied(n LifecycleNotifier) {
	d.notify = n
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

	// The reconcile with the control plane's daemon set. Applied on the
	// subscription goroutine rather than detached: snapshots arrive one per
	// owner per sweep, and applying them in order keeps two snapshots for the
	// same owner from interleaving.
	snapSub, err := d.nc.QueueSubscribe(SubjectRegistrySnapshot, registrySnapshotQueue, func(msg *nats.Msg) {
		d.handleSnapshot(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("daemonstate: subscribe %s: %w", SubjectRegistrySnapshot, err)
	}
	defer func() { _ = snapSub.Unsubscribe() }()

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
	case EventLifecycle:
		return d.onLifecycle(ctx, evt)
	case EventRemoved:
		return d.onRemoved(ctx, evt)
	default:
		return fmt.Errorf("unknown event type %q", evt.Type)
	}
}

// onLifecycle mirrors a control-plane lifecycle observation onto the daemons
// row. Unlike the three liveness handlers, this one writes state this service
// does not own: the Workspace CR is authoritative and only the control-plane
// operator watches it.
//
// Two things are deliberately NOT errors, because treating them as failures
// would turn normal operation into log noise:
//
//   - An unrecognized phase is dropped. The registry then falls back to
//     attachment-derived status, which is always correct if less specific —
//     better than storing a phase no reader can interpret.
//   - No row updated. Either the event is stale (superseded by a newer
//     transition, which the SQL guard exists to drop) or it carried no
//     identity for a daemon that has never registered here. The registry
//     snapshot fills that in.
//
// An event that carries the owner (HasIdentity) first upserts the identity
// row, so it can CREATE the machine's registry row. The control plane sends
// one at create time, which is what makes a machine appear in the list the
// moment it is created, in its provisioning state and under its own name —
// rather than minutes later, when its pod first reaches the gateway.
func (d *Derivation) onLifecycle(ctx context.Context, evt Event) error {
	if !ValidLifecyclePhase(evt.Phase) {
		logging.Warn(logPrefix+" lifecycle event with unknown phase, dropping",
			"daemonID", evt.DaemonID, "phase", string(evt.Phase))
		return nil
	}

	created := false
	if evt.HasIdentity() {
		ident := db.DaemonIdentity{
			DaemonID:   evt.DaemonID,
			UserID:     evt.UserID,
			Name:       evt.Name,
			DaemonType: evt.DaemonType,
		}
		if evt.CreatedAt != nil {
			ident.CreatedAt = *evt.CreatedAt
		}
		wrote, err := d.repo.UpsertDaemonIdentity(ctx, ident)
		if err != nil {
			return fmt.Errorf("upsert identity %s: %w", evt.DaemonID, err)
		}
		created = wrote
	}

	updated, err := d.repo.ApplyDaemonLifecycle(ctx, db.DaemonLifecycleUpdate{
		DaemonID:        evt.DaemonID,
		Phase:           string(evt.Phase),
		Size:            evt.Size,
		StatusMessage:   evt.StatusMessage,
		ChangedAt:       evt.At,
		LastOOMKilledAt: evt.LastOOMKilledAt,
		OOMKillCount:    evt.OOMKillCount,
	})
	if err != nil {
		return fmt.Errorf("apply lifecycle %s: %w", evt.DaemonID, err)
	}
	if !updated && !created {
		logging.Debug(logPrefix+" lifecycle event did not apply (stale or daemon not registered)",
			"daemonID", evt.DaemonID, "phase", string(evt.Phase), "at", evt.At)
		return nil
	}
	// Only on an applied event. Every gateway replica consumes every event,
	// and the newest-wins guard lets exactly one of them apply it, so this
	// fires once per transition rather than once per replica.
	if d.notify != nil {
		d.notify(ctx, evt.UserID, evt.DaemonID)
	}
	return nil
}

// onRemoved deletes the registry row of a daemon the control plane deleted.
// Idempotent: a removal for a row that is already gone (or never existed) is
// not an error. The owner's clients are told only when a row actually went,
// using the owner the row itself recorded — the event need not carry one.
func (d *Derivation) onRemoved(ctx context.Context, evt Event) error {
	userID, removed, err := d.repo.RemoveDaemon(ctx, evt.DaemonID)
	if err != nil {
		return fmt.Errorf("remove daemon %s: %w", evt.DaemonID, err)
	}
	if !removed {
		return nil
	}
	logging.Info(logPrefix+" removed daemon deleted by the control plane",
		"daemonID", evt.DaemonID, "userID", userID)
	if d.notify != nil {
		d.notify(ctx, userID, evt.DaemonID)
	}
	return nil
}

// handleSnapshot applies one owner's authoritative daemon set. See
// db.Repo.ApplyDaemonRegistrySnapshot for the guards that make removal safe.
func (d *Derivation) handleSnapshot(ctx context.Context, msg *nats.Msg) {
	var snap RegistrySnapshot
	if err := json.Unmarshal(msg.Data, &snap); err != nil {
		logging.Warn(logPrefix+" malformed registry snapshot, dropping", "error", err)
		return
	}
	if snap.UserID == "" || snap.At.IsZero() {
		logging.Warn(logPrefix + " registry snapshot missing user_id or at, dropping")
		return
	}
	result, err := d.repo.ApplyDaemonRegistrySnapshot(ctx, snapshotApply(snap, time.Now().UTC()))
	if err != nil {
		logging.Warn(logPrefix+" registry snapshot failed; the next one retries",
			"userID", snap.UserID, "error", err)
		return
	}
	if !result.Changed() {
		return
	}
	logging.Info(logPrefix+" registry snapshot reconciled daemons",
		"userID", snap.UserID, "upserted", result.Upserted, "removed", result.Removed)
	if d.notify != nil {
		// One notification per owner: the client refetches the whole list,
		// so naming each daemon would only multiply identical refetches.
		daemonID := ""
		if len(result.Removed) > 0 {
			daemonID = result.Removed[0]
		} else if len(result.Upserted) > 0 {
			daemonID = result.Upserted[0]
		}
		d.notify(ctx, snap.UserID, daemonID)
	}
}

// snapshotApply translates the wire snapshot into the repository's terms.
// Daemons with no id are dropped; an entry with an unrecognised phase keeps
// its identity but carries no lifecycle, the same rule onLifecycle applies.
func snapshotApply(snap RegistrySnapshot, now time.Time) db.RegistrySnapshotApply {
	apply := db.RegistrySnapshotApply{
		UserID:            snap.UserID,
		Daemons:           make([]db.RegistrySnapshotDaemon, 0, len(snap.Daemons)),
		KeepCreatedAfter:  snap.At.Add(-registrySnapshotGrace),
		KeepAttachedSince: now.Add(-registryAttachmentFreshness),
	}
	for _, rd := range snap.Daemons {
		if rd.DaemonID == "" {
			continue
		}
		entry := db.RegistrySnapshotDaemon{Identity: db.DaemonIdentity{
			DaemonID:   rd.DaemonID,
			UserID:     snap.UserID,
			Name:       rd.Name,
			DaemonType: rd.DaemonType,
			CreatedAt:  rd.CreatedAt,
		}}
		if rd.Phase != "" && ValidLifecyclePhase(rd.Phase) {
			changedAt := rd.CreatedAt
			if rd.PhaseChangedAt != nil {
				changedAt = *rd.PhaseChangedAt
			}
			if !changedAt.IsZero() {
				entry.Lifecycle = &db.DaemonLifecycleUpdate{
					DaemonID:        rd.DaemonID,
					Phase:           string(rd.Phase),
					Size:            rd.Size,
					StatusMessage:   rd.StatusMessage,
					ChangedAt:       changedAt,
					LastOOMKilledAt: rd.LastOOMKilledAt,
					OOMKillCount:    rd.OOMKillCount,
				}
			}
		}
		apply.Daemons = append(apply.Daemons, entry)
	}
	return apply
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
