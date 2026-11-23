// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package daemonmirror

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The daemons table is a read replica of the control plane's daemon set for
// everything except liveness (daemon_attachment, which this service owns).
// The control plane is the authority on which machines EXIST, what they are
// called, and what lifecycle phase they are in; this package holds the three
// writes that keep the replica honest about that:
//
//   - UpsertDaemonIdentity: a machine exists (fast path, from an
//     identity-bearing lifecycle event at create time).
//   - RemoveDaemon: a machine no longer exists (fast path, from a removed
//     event at delete time).
//   - ApplyDaemonRegistrySnapshot: the reconcile. One owner's full set, which
//     repairs whatever the two fast paths lost.
//
// Before these existed the replica could only ever grow, and only when a
// machine's pod reached the gateway: a deleted machine stayed listed forever,
// and a new one was invisible for the minutes it took to boot
// (MACHINE_LIST_BUGS_2026-10-07).

// txPolicy is the level both removals and the snapshot run at: READ COMMITTED
// with the default attempts, as the facade's RunTx call did.
var txPolicy = orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)

// Deps holds dependencies for the daemonmirror package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Daemons holds the registry rows and what is bound to them.
	Daemons Daemons
	// Tx opens the transaction a removal or a snapshot runs in.
	Tx TxRunner
	// Lifecycle applies a control-plane lifecycle event to a registry row.
	Lifecycle Lifecycle
	// Updates tells the owner's open clients a chat they had open changed.
	Updates UserUpdates
}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new daemonmirror service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Daemons == nil || deps.Tx == nil || deps.Lifecycle == nil || deps.Updates == nil {
		return nil, errors.New("daemonmirror: Deps.Daemons, Tx, Lifecycle and Updates are required")
	}
	return &service{deps: deps}, nil
}

// RegistrySnapshotApply is one owner's authoritative daemon set, as applied by
// ApplyDaemonRegistrySnapshot.
type RegistrySnapshotApply struct {
	UserID  string
	Daemons []RegistrySnapshotDaemon
	// KeepCreatedAfter protects rows this registry created recently from
	// removal even when the snapshot does not list them. The control plane
	// learns of a self-hosted daemon only after the gateway has registered
	// it here, so a snapshot read in that gap would otherwise delete a daemon
	// that is seconds old. Callers pass the snapshot's read time minus a
	// grace period.
	KeepCreatedAfter time.Time
	// KeepAttachedSince protects rows with a live attachment lease: a daemon
	// attached right now is evidently real, whatever the snapshot says, and
	// deleting its row would hide a connected machine. Callers pass now minus
	// the attachment freshness window.
	KeepAttachedSince time.Time
}

// RegistrySnapshotDaemon is one daemon in a RegistrySnapshotApply. Lifecycle
// is nil for daemons with none to mirror (every self-hosted daemon).
type RegistrySnapshotDaemon struct {
	Identity  core.DaemonIdentity
	Lifecycle *core.DaemonLifecycleUpdate
}

// RegistrySnapshotResult reports what a snapshot changed, so the caller can
// tell the owner's clients to refetch only when something did.
type RegistrySnapshotResult struct {
	Upserted []string
	Removed  []string
}

// Changed reports whether the snapshot changed any row.
func (r RegistrySnapshotResult) Changed() bool {
	return len(r.Upserted) > 0 || len(r.Removed) > 0
}

// UpsertDaemonIdentity creates the registry row for a daemon the control plane
// reports, or brings the name on an existing row up to date. Returns whether a
// row was written.
//
// Deliberately narrow on conflict:
//
//   - user_id is NEVER rewritten. A row moving between owners on the strength
//     of an event is not something a mirror should do in place; if the two
//     sides ever disagree, the snapshot removes the row from the wrong owner
//     and creates it for the right one, each step scoped to one owner.
//   - Hostname, platform and the rest are the daemon's own registration and
//     are left to UpsertDaemon.
//   - daemon_type is filled only when the row has none.
//
// The conflict update is conditional, so an unchanged daemon costs a no-op
// rather than a write: the snapshot re-asserts every daemon on every sweep,
// and rewriting updated_at each time would reorder the list for nothing.
func (s *service) UpsertDaemonIdentity(ctx context.Context, id core.DaemonIdentity) (bool, error) {
	if id.DaemonID == "" {
		return false, fmt.Errorf("daemon ID cannot be empty")
	}
	if id.UserID == "" {
		return false, fmt.Errorf("daemon user ID cannot be empty")
	}
	if strings.EqualFold(strings.TrimSpace(id.Name), id.DaemonID) {
		// The control plane names a self-hosted machine that connected with
		// neither a name nor a hostname by its own id: a placeholder that
		// keeps its per-owner unique name index satisfied, not a name anyone
		// chose. Recorded here, it was every sentence's name for the machine
		// ("reliant-labs isn't on \"46e3682b-…\""). No name is written
		// instead, so the readers fall back to the hostname or a short tag,
		// and a real name already on the row is kept.
		id.Name = ""
	}
	now := time.Now().UTC()
	createdAt := id.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	wrote, err := s.deps.Daemons.UpsertDaemonIdentity(ctx, id, createdAt, now)
	if err != nil {
		return false, fmt.Errorf("failed to upsert daemon identity %s: %w", id.DaemonID, err)
	}
	return wrote, nil
}

// RemoveDaemon deletes a daemon's registry row and its attachment lease, and
// returns the owner the row belonged to so the caller can tell that owner's
// clients. removed is false when there was no row: removal is idempotent,
// and a removed event for a daemon this registry never heard of is normal.
//
// The attachment goes too: a lease for a daemon that does not exist is
// meaningless, and a stale one would keep the reap loop as its only cleanup.
// Connector grants go with the row through their ON DELETE CASCADE, which is
// right: a grant to a machine that no longer exists grants nothing. Every
// other binding to the machine is released in the same transaction
// (releaseDaemonBindings).
func (s *service) RemoveDaemon(ctx context.Context, daemonID string) (string, bool, error) {
	if daemonID == "" {
		return "", false, fmt.Errorf("daemon ID cannot be empty")
	}
	var userID string
	var removed bool
	err := s.deps.Tx.RunTx(ctx, txPolicy, func(txCtx context.Context) error {
		var err error
		userID, removed, err = s.removeDaemon(txCtx, daemonID)
		return err
	})
	if err != nil {
		return "", false, err
	}
	return userID, removed, nil
}

// removeDaemon deletes the row and its lease and releases its bindings. ctx
// must carry the caller's transaction, so the release commits or rolls back
// with the removal.
func (s *service) removeDaemon(ctx context.Context, daemonID string) (string, bool, error) {
	userID, removed, err := s.deps.Daemons.DeleteDaemonRow(ctx, daemonID)
	if err != nil || !removed {
		return "", false, err
	}
	if err := s.releaseDaemonBindings(ctx, daemonID); err != nil {
		return "", false, err
	}
	return userID, true, nil
}

// releaseDaemonBindings drops every reference to a daemon that no longer
// exists. Three rows name a machine:
//
//   - chats.active_daemon_id, the chat's pin;
//   - worktrees.daemon_id, the machine holding a workspace's checkout;
//   - project_daemons, the project installed on a machine.
//
// Each resolves to a daemon nothing can reach once the machine is gone, so a
// chat on a deleted machine, or on one that came back under a new id, never
// recovered: its tools routed to the dead id forever, and
// daemonchoice.Validate refused every OTHER machine for a project whose
// only install was on it. Released, each is re-learned from live evidence:
// the chat falls to its worktree's owner, else default resolution (exactly as
// a chat that never had a pin), a workspace is adopted by the machine that
// finds its directory (worktreesweep -> AdoptWorktreeDaemon), and an install
// is re-recorded by the machine that has the checkout when it connects
// (reconcileProjectDaemons).
//
// Clearing a pin never sets no_machine: a chat on a machine stays on one.
// Open clients hear of the unpinned chat through the same
// chat_config_changed update SetChatDaemon sends.
func (s *service) releaseDaemonBindings(ctx context.Context, daemonID string) error {
	chats, err := s.deps.Daemons.UnpinChatsFromDaemon(ctx, daemonID)
	if err != nil {
		return err
	}
	if err := s.deps.Daemons.ReleaseDaemonFromWorkspaces(ctx, daemonID); err != nil {
		return err
	}

	for _, c := range chats {
		data, err := json.Marshal(map[string]interface{}{"chat_id": c.ID, "active_daemon_id": ""})
		if err != nil {
			return err
		}
		update := &core.UserUpdate{
			UserID:     c.UserID,
			ProjectID:  &c.ProjectID,
			ChatID:     &c.ID,
			WorktreeID: c.WorktreeID,
			UpdateType: userUpdateChatConfigChanged,
			EntityType: entityTypeChat,
			EntityID:   c.ID,
			Data:       data,
		}
		if err := s.deps.Updates.CreateUserUpdate(ctx, update); err != nil {
			return fmt.Errorf("announcing chat %s unpinned from removed daemon %s: %w", c.ID, daemonID, err)
		}
	}
	return nil
}

// ApplyDaemonRegistrySnapshot reconciles one owner's registry rows against the
// control plane's authoritative set, in one transaction:
//
//  1. every daemon in the set gets its identity upserted (created if missing),
//     and its lifecycle applied through the same newest-wins guard as a
//     lifecycle event;
//  2. every row of this owner NOT in the set is removed, unless it was
//     created after KeepCreatedAfter or holds an attachment lease fresher than
//     KeepAttachedSince. Those two guards are what make "absent from the
//     control plane's set" safe to act on: the control plane hears about a
//     self-hosted daemon only after it has registered here, and a connected
//     daemon is real whatever a snapshot read a moment ago says.
//
// Scoped to one owner throughout. Rows of other owners are never read or
// written, so a snapshot can only ever affect the user it names.
func (s *service) ApplyDaemonRegistrySnapshot(ctx context.Context, snap RegistrySnapshotApply) (RegistrySnapshotResult, error) {
	if snap.UserID == "" {
		return RegistrySnapshotResult{}, fmt.Errorf("snapshot user ID cannot be empty")
	}
	var result RegistrySnapshotResult
	err := s.deps.Tx.RunTx(ctx, txPolicy, func(txCtx context.Context) error {
		var err error
		result, err = s.applyDaemonRegistrySnapshot(txCtx, snap)
		return err
	})
	if err != nil {
		return RegistrySnapshotResult{}, fmt.Errorf("registry snapshot for %s: %w", snap.UserID, err)
	}
	return result, nil
}

// applyDaemonRegistrySnapshot is one attempt at the snapshot, inside the
// transaction ctx carries. It builds its result from scratch, so a retried
// attempt reports only what the committed one did.
func (s *service) applyDaemonRegistrySnapshot(ctx context.Context, snap RegistrySnapshotApply) (RegistrySnapshotResult, error) {
	var result RegistrySnapshotResult
	keep := make([]string, 0, len(snap.Daemons))
	for _, d := range snap.Daemons {
		ident := d.Identity
		ident.UserID = snap.UserID
		keep = append(keep, ident.DaemonID)

		wrote, err := s.UpsertDaemonIdentity(ctx, ident)
		if err != nil {
			return RegistrySnapshotResult{}, err
		}
		if d.Lifecycle != nil {
			lc := *d.Lifecycle
			lc.DaemonID = ident.DaemonID
			applied, err := s.deps.Lifecycle.ApplyDaemonLifecycle(ctx, lc)
			if err != nil {
				return RegistrySnapshotResult{}, err
			}
			wrote = wrote || applied
		}
		if wrote {
			result.Upserted = append(result.Upserted, ident.DaemonID)
		}
	}

	stale, err := s.deps.Daemons.LockDaemonsAbsentFromSnapshot(ctx, snap.UserID, keep, snap.KeepCreatedAfter, snap.KeepAttachedSince)
	if err != nil {
		return RegistrySnapshotResult{}, err
	}
	for _, id := range stale {
		if _, removed, err := s.removeDaemon(ctx, id); err != nil {
			return RegistrySnapshotResult{}, err
		} else if removed {
			result.Removed = append(result.Removed, id)
		}
	}
	return result, nil
}
