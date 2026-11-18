package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/store"
)

// A PROJECT DETACHED FROM A MACHINE STAYS DETACHED UNTIL THE USER SAYS OTHERWISE.
//
// DetachProjectFromDaemon forgets a checkout without deleting it: the folder
// stays on the machine's disk. Dropping the project_daemons row alone would
// not stick — the machine's next connect finds the folder and records it again
// (the reconcile), the plain-folder heal recreates it, and the machine's
// config watcher keeps pushing the project's snapshots. The detachment is the
// record each of those reads to leave the pair alone.
//
// A pair (project, machine) has at most one of a project_daemons row and a
// detachment: detaching deletes the row and records the detachment in one
// transaction, and recording a checkout (UpsertProjectDaemon,
// UpsertQueuedProjectDaemon — every attach, create and clone) deletes the
// detachment in the same transaction as it writes the row.

// DetachProjectFromDaemon deletes the project's checkout record on the
// machine and that machine's synced config of the project, and records the
// detachment at path (the checkout path it had there), in one transaction.
// The project's config union is rewritten without the machine's catalog,
// under the same advisory lock every catalog writer takes
// (SyncProjectConfigSnapshot) and every checkout writer takes
// (UpsertProjectDaemon, RecordObservedProjectDaemonInScope). Nothing is sent to the
// machine.
//
// guard, when not nil, runs first inside that transaction, after the lock:
// the caller's preconditions (another checkout remains, no workspaces live
// there) are judged against every detach and checkout write that committed
// before it, so two concurrent detaches cannot each see the other's checkout
// and leave none. A guard error aborts the detach and is returned as is;
// ErrNothingToDetach aborts it as a success.
func (r *Repo) DetachProjectFromDaemon(ctx context.Context, projectID, daemonID, path string, guard func(ctx context.Context) error) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		if err := r.lockCheckoutForDetach(txCtx, projectID, daemonID); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(txCtx); err != nil {
				return err
			}
		}
		if err := store.RecordProjectDaemonDetachment(txCtx, r.ORM(), projectID, daemonID, path); err != nil {
			return err
		}
		return r.removeCheckoutLocked(txCtx, projectID, daemonID)
	})
	if errors.Is(err, ErrNothingToDetach) {
		return nil
	}
	return err
}

// DetachProjectDaemonOfOtherOrg is the machine-org cleanup of one checkout:
// when the project is in ANOTHER org than machineOrg, the machine's, right
// now, it detaches the project from the machine as DetachProjectFromDaemon
// does — the project_daemons row, the machine's synced config of the project
// (project_machine_catalogs, which the project's config union is rewritten
// without), under the same locks, through the same removal
// (removeCheckoutLocked) — after recording both rows whole in
// project_daemon_org_cleanups, so dev-docs/machine-org-rollout.md can put
// them back. It reports whether a checkout went.
//
// Two things differ from a user's detach, on purpose. It records no
// detachment: the platform, not the user, removed the record, and a project
// moved back into the machine's org is the machine's to find again. And the
// org is decided here, not from the caller's earlier read: the project row is
// locked FOR SHARE (a project's org move takes it FOR NO KEY UPDATE, and none
// of the detach's advisory locks, so the order cannot deadlock it) and its
// org re-read. A project now in the machine's org keeps its checkout, and so
// does one with no org yet (it is its owner's default org's, lazily; only a
// stamped org is authoritative enough to delete over). False (gone already,
// or no longer of another org) is not an error, so the cleanup is idempotent.
func (r *Repo) DetachProjectDaemonOfOtherOrg(ctx context.Context, projectID, daemonID, userID, machineOrg string) (bool, error) {
	if projectID == "" || daemonID == "" || machineOrg == "" {
		return false, fmt.Errorf("project, daemon and machine org are required")
	}
	detached := false
	err := r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		detached = false
		if err := r.lockCheckoutForDetach(txCtx, projectID, daemonID); err != nil {
			return err
		}
		// A project scheduled for deletion is detached all the same: the
		// cleanup removes, never adds, and the audit keeps what it removed
		// for a restore of either.
		org, _, found, err := store.LockProjectOrgForShare(txCtx, r.ORM(), projectID)
		if err != nil || !found || org == nil || *org == machineOrg {
			return err
		}
		recorded, err := store.RecordProjectDaemonOrgCleanup(txCtx, r.ORM(), projectID, daemonID, userID, machineOrg, org)
		if err != nil || !recorded {
			return err
		}
		if err := r.removeCheckoutLocked(txCtx, projectID, daemonID); err != nil {
			return err
		}
		detached = true
		return nil
	})
	return detached, err
}

// lockCheckoutForDetach takes, in order, every lock a detach of the
// project's checkout on the machine holds. ctx must carry the transaction.
func (r *Repo) lockCheckoutForDetach(ctx context.Context, projectID, daemonID string) error {
	if err := store.LockProjectCatalog(ctx, r.ORM(), projectID); err != nil {
		return err
	}
	// Then the machine's: a path the machine reports is resolved under it
	// (WithMachineCheckoutsLocked), so that resolution sees this detach
	// whole or not at all. Always taken second, after the catalog lock; the
	// resolution takes no catalog lock while it holds this one.
	if err := store.LockMachineCheckouts(ctx, r.ORM(), daemonID); err != nil {
		return err
	}
	// Then the checkout's, exclusive: a branch workspace of it is inserted
	// under the shared side (CreateWorktree), so a guard sees every one that
	// committed and none lands until the detach has.
	return store.LockProjectCheckout(ctx, r.ORM(), projectID, daemonID, false)
}

// removeCheckoutLocked is what detaching a project from a machine removes:
// the checkout record, the machine's synced config of the project, and that
// config's share of the project's config union, which is rewritten from the
// machines left. ctx must carry the transaction holding
// lockCheckoutForDetach's locks.
func (r *Repo) removeCheckoutLocked(ctx context.Context, projectID, daemonID string) error {
	if err := store.RemoveProjectDaemon(ctx, r.ORM(), projectID, daemonID); err != nil {
		return err
	}
	if err := store.DeleteProjectMachineCatalog(ctx, r.ORM(), projectID, daemonID); err != nil {
		return err
	}
	return r.rewriteProjectCatalogUnion(ctx, projectID)
}

// ErrNothingToDetach is what a DetachProjectFromDaemon guard returns when the
// checkout is already gone (another detach got there first): the detach ends
// as a success, recording nothing.
var ErrNothingToDetach = errors.New("no checkout to detach")

// WithMachineCheckoutsLocked runs fn in a transaction holding the machine's
// checkout lock, the one DetachProjectFromDaemon takes: every read and write
// fn makes through this Repo with the ctx it is handed joins that
// transaction, so a detach from the machine commits wholly before fn's reads
// or after its writes. fn must not take a project's catalog lock (the detach
// takes that one first). It may run more than once, on a deadlock retry.
func (r *Repo) WithMachineCheckoutsLocked(ctx context.Context, daemonID string, fn func(ctx context.Context) error) error {
	if daemonID == "" {
		return fmt.Errorf("daemon ID cannot be empty")
	}
	return r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		if err := store.LockMachineCheckouts(txCtx, r.ORM(), daemonID); err != nil {
			return err
		}
		return fn(txCtx)
	})
}

// ListProjectDaemonDetachmentsForDaemon returns every project detached from
// the machine.
func (r *Repo) ListProjectDaemonDetachmentsForDaemon(ctx context.Context, daemonID string) ([]*ProjectDaemonDetachment, error) {
	if daemonID == "" {
		return nil, fmt.Errorf("daemon ID cannot be empty")
	}
	return store.ListProjectDaemonDetachmentsForDaemon(ctx, r.ORM(), daemonID)
}

// rewriteProjectCatalogUnion recomputes the union columns of the project's
// config record from the machines' catalogs as they are now. ctx must carry
// the transaction holding LockProjectCatalog.
func (r *Repo) rewriteProjectCatalogUnion(ctx context.Context, projectID string) error {
	catalogs, err := r.projectMachineCatalogs(ctx, projectID)
	if err != nil {
		return err
	}
	union, err := r.projectFilesUnion(ctx, projectID)
	if err != nil {
		return err
	}
	if union.WorkflowsJSON, err = config.EncodeCatalogJSON(config.UnionWorkflows(catalogs)); err != nil {
		return err
	}
	if union.PresetsJSON, err = config.EncodeCatalogJSON(config.UnionPresets(catalogs)); err != nil {
		return err
	}
	return store.SetProjectConfigCatalogUnion(ctx, r.ORM(), projectID, union, time.Now().UTC())
}
