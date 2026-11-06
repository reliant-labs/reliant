package db

import (
	"context"
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
// (SyncProjectConfigSnapshot). Nothing is sent to the machine.
func (r *Repo) DetachProjectFromDaemon(ctx context.Context, projectID, daemonID, path string) error {
	if projectID == "" || daemonID == "" {
		return fmt.Errorf("project ID and daemon ID cannot be empty")
	}
	return r.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), func(txCtx context.Context) error {
		if err := store.LockProjectCatalog(txCtx, r.ORM(), projectID); err != nil {
			return err
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
