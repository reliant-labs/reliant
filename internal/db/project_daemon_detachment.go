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
// (UpsertProjectDaemon, RecordObservedProjectDaemon). Nothing is sent to the
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
		if err := store.LockProjectCatalog(txCtx, r.ORM(), projectID); err != nil {
			return err
		}
		// Then the machine's: a path the machine reports is resolved under
		// it (WithMachineCheckoutsLocked), so that resolution sees this
		// detach whole or not at all. Always taken second, after the
		// catalog lock; the resolution takes no catalog lock while it holds
		// this one.
		if err := store.LockMachineCheckouts(txCtx, r.ORM(), daemonID); err != nil {
			return err
		}
		// Then the checkout's, exclusive: a branch workspace of it is inserted
		// under the shared side (CreateWorktree), so the guard sees every one
		// that committed and none lands until the detach has.
		if err := store.LockProjectCheckout(txCtx, r.ORM(), projectID, daemonID, false); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(txCtx); err != nil {
				return err
			}
		}
		if err := store.RemoveProjectDaemon(txCtx, r.ORM(), projectID, daemonID); err != nil {
			return err
		}
		if err := store.DeleteProjectMachineCatalog(txCtx, r.ORM(), projectID, daemonID); err != nil {
			return err
		}
		if err := store.RecordProjectDaemonDetachment(txCtx, r.ORM(), projectID, daemonID, path); err != nil {
			return err
		}
		return r.rewriteProjectCatalogUnion(txCtx, projectID)
	})
	if errors.Is(err, ErrNothingToDetach) {
		return nil
	}
	return err
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

// TearDownProjectCheckout runs an action that destroys the project's
// checkout on the machine (a remove, a reclone): guard, then destroy, which
// sends the daemon command. A branch workspace of the checkout is a git
// worktree of it, so none may be admitted from guard's read until the machine
// has finished deleting. Two things hold them back:
//
//   - the checkout's exclusive lock, held in one transaction across guard and
//     destroy. A branch workspace is inserted under the shared side
//     (CreateWorktree), so guard sees every workspace that committed and no
//     new one lands while destroy waits for the machine;
//   - project_daemons.teardown_pending_at, committed on its own after guard
//     passes and before destroy runs, and cleared only when destroy reports
//     the outcome settled (the machine answered, or the command provably never
//     reached it). CreateWorktree refuses while it is set
//     (core.ErrCheckoutTeardownPending). The lock ends with the transaction,
//     and the machine does not stop deleting when the server stops waiting: a
//     timeout, or the server dying mid-wait, leaves the mark, so admission
//     stays refused until a remove or reclone run again settles it.
//
// destroy is told whether the mark was already set when it started
// (earlierUnknown): an earlier remove or reclone's outcome was never heard,
// and the machine may still be running it. Its settled then has to cover that
// earlier command too, not only its own: a retry that never reached the
// machine, or one that ran beside the earlier delete, says nothing about it.
//
// guard and destroy's reads and writes through this Repo with the ctx they
// are handed join the transaction. That ctx is not cancelled with ctx: a
// caller going away does not cut the wait for the machine short (destroy's
// own request budget bounds it). It runs once (no retry): it sends a daemon
// command.
func (r *Repo) TearDownProjectCheckout(ctx context.Context, projectID, daemonID string, guard func(ctx context.Context) error, destroy func(ctx context.Context, earlierUnknown bool) (settled bool)) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	base := context.WithoutCancel(ctx)
	return r.RunTx(base, orm.ReadCommitted, func(txCtx context.Context) error {
		if err := store.LockProjectCheckout(txCtx, r.ORM(), projectID, daemonID, false); err != nil {
			return err
		}
		if guard != nil {
			if err := guard(txCtx); err != nil {
				return err
			}
		}
		earlierUnknown, err := store.ProjectCheckoutTeardownPending(txCtx, r.ORM(), projectID, daemonID)
		if err != nil {
			return err
		}
		// base carries no transaction: the mark commits now, so it outlives
		// this transaction whatever becomes of it.
		if err := store.SetProjectCheckoutTeardownPending(base, r.ORM(), projectID, daemonID, true); err != nil {
			return err
		}
		if !destroy(txCtx, earlierUnknown) {
			return nil
		}
		return store.SetProjectCheckoutTeardownPending(txCtx, r.ORM(), projectID, daemonID, false)
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
