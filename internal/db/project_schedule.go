// Copyright (c) 2025 Reliant Labs

package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

// A project's deletion with a restore window (control-plane
// docs/design/unified-shell-and-projects.md §11.3, D18;
// internal/store/project_schedule.go). The caller decides who may
// (authz.ProjectDelete); these record and execute it.

// ScheduleProjectDeletion hides the project until deleteAfter, when the
// purge worker deletes it, and pauses its enabled triggers, in one
// transaction. A project already scheduled keeps its first date. The
// returned trigger ids are the ones paused, whose schedules the caller
// converges.
func (r *Repo) ScheduleProjectDeletion(ctx context.Context, id, by string, deleteAfter time.Time) (core.ProjectDeletion, error) {
	if id == "" {
		return core.ProjectDeletion{}, fmt.Errorf("project ID cannot be empty")
	}
	var out core.ProjectDeletion
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		out, err = store.ScheduleProjectDeletion(ctx, r.ORM(), id, by, deleteAfter)
		return err
	})
	return out, err
}

// RestoreProject cancels the project's scheduled deletion and turns back on
// the triggers it paused, returning their ids. A project not scheduled is a
// no-op; one whose purge began is core.ErrProjectPurgeStarted.
func (r *Repo) RestoreProject(ctx context.Context, id string) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	var resumed []string
	err := r.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		resumed, err = store.RestoreProjectDeletion(ctx, r.ORM(), id)
		return err
	})
	return resumed, err
}

// PurgeProjectNow deletes userID's project at once, scheduled or not
// (store.DeleteUserProject's batches): it is claimed first, so it is hidden
// and can no longer be restored while the batches run, and a purge that
// stops part way is finished by the purge worker. It returns the ids of the
// triggers it deleted, whose schedules the caller removes. A project that is
// missing or another user's is core.ErrProjectNotFound.
func (r *Repo) PurgeProjectNow(ctx context.Context, id, userID string) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	claimed, err := store.ClaimProjectPurgeNow(ctx, r.ORM(), id, userID, time.Now())
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, core.ErrProjectNotFound
	}
	return r.purgeProject(ctx, id, userID)
}

// PurgeDueProject purges the project if its scheduled deletion is due and no
// other purge holds it within lease: it reports whether it purged it, and
// the ids of the triggers it deleted.
func (r *Repo) PurgeDueProject(ctx context.Context, id string, now time.Time, lease time.Duration) (bool, []string, error) {
	owner, claimed, err := store.ClaimProjectPurge(ctx, r.ORM(), id, now, lease)
	if err != nil || !claimed {
		return false, nil, err
	}
	triggers, err := r.purgeProject(ctx, id, owner)
	if err != nil {
		return false, nil, err
	}
	return true, triggers, nil
}

func (r *Repo) purgeProject(ctx context.Context, id, owner string) ([]string, error) {
	triggers, err := store.ListProjectTriggerIDs(ctx, r.ORM(), id)
	if err != nil {
		return nil, err
	}
	if err := store.DeleteUserProject(ctx, r.ORM(), id, owner); err != nil {
		return nil, err
	}
	return triggers, nil
}

// ListDueProjectPurges lists up to limit projects whose scheduled deletion
// is due and that no purge holds within lease.
func (r *Repo) ListDueProjectPurges(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]string, error) {
	return store.ListDueProjectPurges(ctx, r.ORM(), now, lease, limit)
}

// ListDeletedProjects lists the scheduled, restorable projects userID owns
// or deleted, in org (nil: every org).
func (r *Repo) ListDeletedProjects(ctx context.Context, userID string, org *core.OrgScope) ([]*core.Project, error) {
	if userID == "" {
		return nil, errors.New("user ID cannot be empty")
	}
	return store.ListDeletedProjects(ctx, r.ORM(), userID, org)
}

// ProjectPurgeResult is what one pass of PurgeDueProjects did.
type ProjectPurgeResult struct {
	// Purged is how many projects it deleted; Failed how many it could not
	// (each is taken again once its claim's lease runs out).
	Purged, Failed int
	// OrphansSwept is how many rows naming a gone project it removed.
	OrphansSwept int64
}

// PurgeDueProjects is one pass of the purge worker at now: it purges up to
// limit projects whose scheduled deletion is due, each claimed for lease
// first, then sweeps up to limit rows per table that name a project that is
// gone. A project that fails is logged and left for a later pass; the error
// is only a pass that could not list what is due.
func (r *Repo) PurgeDueProjects(ctx context.Context, now time.Time, lease time.Duration, limit int) (ProjectPurgeResult, error) {
	var res ProjectPurgeResult
	ids, err := r.ListDueProjectPurges(ctx, now, lease, limit)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		purged, _, err := r.PurgeDueProject(ctx, id, now, lease)
		switch {
		case err != nil:
			res.Failed++
			logging.WarnContext(ctx, "project purge: could not purge a project scheduled for deletion; a later pass resumes it",
				"project_id", id, "error", err)
		case purged:
			res.Purged++
			logging.InfoContext(ctx, "project purge: purged a project whose restore window ended", "project_id", id)
		}
	}
	n, err := r.SweepOrphanedProjectRows(ctx, limit)
	res.OrphansSwept = n
	if err != nil {
		logging.WarnContext(ctx, "project purge: could not sweep rows of deleted projects", "error", err)
	}
	return res, nil
}

// SweepOrphanedProjectRows removes up to batch rows per table that name a
// project that no longer exists, with no foreign key to cascade them.
func (r *Repo) SweepOrphanedProjectRows(ctx context.Context, batch int) (int64, error) {
	return store.SweepOrphanedProjectRows(ctx, r.ORM(), batch)
}
