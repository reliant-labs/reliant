// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A project's deletion, with a restore window (control-plane
// docs/design/unified-shell-and-projects.md §11.3, D18).
//
// Scheduling sets projects.delete_after and pauses the project's enabled
// triggers (core.TriggerPausedProjectDeleted) in one transaction. Restoring
// clears it and turns those triggers, and only those, back on. Once
// delete_after has passed, a purge claims the project (purge_started_at) and
// runs DeleteUserProject's batches; from the claim on, a restore is refused,
// because the batches delete the project's chats a few at a time and a
// restore between two of them would bring back a project missing some.
//
// Every function here that writes takes the project row FOR NO KEY UPDATE
// first: it never changes a key, so it does not wait for (or block) the key
// share every chat, worktree and trigger insert takes on the row, while two
// of these on the same project still wait for each other.

// TriggerChangeProjectRestored is the trigger_changes operation recording a
// trigger turned back on because its project was restored.
const TriggerChangeProjectRestored = "project_restored"

// whereProjectLive keeps the rows whose project, named by column, is not
// scheduled for deletion: the chats (and the like) of a deleted project leave
// a list with it. A primary-key probe per row the list reads.
func whereProjectLive(column string) orm.QueryOption {
	return func(q *bun.SelectQuery) {
		q.Where("NOT EXISTS (SELECT 1 FROM projects dp WHERE dp.id = ? AND dp.delete_after IS NOT NULL)", bun.Ident(column))
	}
}

// ScheduleProjectDeletion schedules the project for purge at deleteAfter,
// recording by as who did, and pauses its enabled triggers. A project already
// scheduled keeps its first date and nothing is written (AlreadyScheduled). A
// missing project is core.ErrProjectNotFound. Run it in a transaction.
func ScheduleProjectDeletion(ctx context.Context, db orm.Context, id, by string, deleteAfter time.Time) (core.ProjectDeletion, error) {
	var rows []struct {
		DeleteAfter *time.Time `bun:"delete_after"`
	}
	if err := db.Bun().NewRaw(`SELECT delete_after FROM projects WHERE id = ?0 FOR NO KEY UPDATE`, id).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return core.ProjectDeletion{}, fmt.Errorf("lock the project: %w", err)
	}
	if len(rows) == 0 {
		return core.ProjectDeletion{}, core.ErrProjectNotFound
	}
	if rows[0].DeleteAfter != nil {
		return core.ProjectDeletion{DeleteAfter: rows[0].DeleteAfter.UTC(), AlreadyScheduled: true}, nil
	}
	if _, err := db.Bun().NewRaw(`UPDATE projects SET delete_after = ?1, deleted_by = ?2, purge_started_at = NULL, updated_at = NOW() WHERE id = ?0`,
		id, deleteAfter.UTC(), by).Exec(ctx); err != nil {
		return core.ProjectDeletion{}, fmt.Errorf("schedule the project's deletion: %w", err)
	}
	paused, err := pauseProjectTriggers(ctx, db, id)
	if err != nil {
		return core.ProjectDeletion{}, err
	}
	return core.ProjectDeletion{DeleteAfter: deleteAfter.UTC(), PausedTriggerIDs: paused}, nil
}

// pauseProjectTriggers pauses each of the project's enabled triggers with
// core.TriggerPausedProjectDeleted, recording each pause in its history, and
// returns their ids. A trigger already paused (by a person or the system)
// keeps its state and reason, so a restore does not turn it on.
func pauseProjectTriggers(ctx context.Context, db orm.Context, projectID string) ([]string, error) {
	ids, err := lockProjectTriggers(ctx, db, projectID, `enabled`)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		before, err := GetTrigger(ctx, db, id)
		if err != nil {
			return nil, err
		}
		if err := PauseTrigger(ctx, db, id, core.TriggerPausedProjectDeleted); err != nil {
			return nil, err
		}
		after, err := GetTrigger(ctx, db, id)
		if err != nil {
			return nil, err
		}
		if err := RecordTriggerChange(ctx, db, "pause", before, after); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// lockProjectTriggers locks the project's triggers matching where (a fixed
// predicate, never input), in id order as every trigger writer does, and
// returns their ids.
func lockProjectTriggers(ctx context.Context, db orm.Context, projectID, where string) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT id FROM triggers WHERE project_id = ?0 AND `+where+` ORDER BY id FOR UPDATE`, projectID).
		Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lock the project's triggers: %w", err)
	}
	return ids, nil
}

// RestoreProjectDeletion cancels the project's scheduled deletion and turns
// back on the triggers it paused, returning their ids. A project that is not
// scheduled is left alone (nothing resumed). One whose purge has begun is
// core.ErrProjectPurgeStarted; a missing one core.ErrProjectNotFound. Run it
// in a transaction.
func RestoreProjectDeletion(ctx context.Context, db orm.Context, id string) ([]string, error) {
	var rows []struct {
		DeleteAfter    *time.Time `bun:"delete_after"`
		PurgeStartedAt *time.Time `bun:"purge_started_at"`
	}
	if err := db.Bun().NewRaw(`SELECT delete_after, purge_started_at FROM projects WHERE id = ?0 FOR NO KEY UPDATE`, id).
		Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lock the project: %w", err)
	}
	switch {
	case len(rows) == 0:
		return nil, core.ErrProjectNotFound
	case rows[0].DeleteAfter == nil:
		return nil, nil
	case rows[0].PurgeStartedAt != nil:
		return nil, core.ErrProjectPurgeStarted
	}
	if _, err := db.Bun().NewRaw(`UPDATE projects SET delete_after = NULL, deleted_by = NULL, purge_started_at = NULL, updated_at = NOW() WHERE id = ?0`, id).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("restore the project: %w", err)
	}
	ids, err := lockProjectTriggers(ctx, db, id, `NOT enabled AND paused_reason = '`+core.TriggerPausedProjectDeleted+`'`)
	if err != nil {
		return nil, err
	}
	for _, tid := range ids {
		before, err := GetTrigger(ctx, db, tid)
		if err != nil {
			return nil, err
		}
		if err := SetTriggerEnabled(ctx, db, tid, true); err != nil {
			return nil, err
		}
		after, err := GetTrigger(ctx, db, tid)
		if err != nil {
			return nil, err
		}
		if err := RecordTriggerChange(ctx, db, TriggerChangeProjectRestored, before, after); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// ListProjectTriggerIDs lists the ids of the project's triggers.
func ListProjectTriggerIDs(ctx context.Context, db orm.Context, projectID string) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT id FROM triggers WHERE project_id = ?0 ORDER BY id`, projectID).
		Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list the project's triggers: %w", err)
	}
	return ids, nil
}

// ClaimProjectPurge claims a scheduled project whose delete_after has passed
// for purging, as of now: purge_started_at is stamped, which from then on
// refuses a restore. A claim older than lease is taken again (its purge died
// part way). It reports the owner the purge deletes as, and false when the
// project is not due, already claimed within lease, restored or gone.
func ClaimProjectPurge(ctx context.Context, db orm.Context, id string, now time.Time, lease time.Duration) (string, bool, error) {
	var owners []string
	if err := db.Bun().NewRaw(`UPDATE projects SET purge_started_at = ?1
WHERE id = ?0 AND delete_after IS NOT NULL AND delete_after <= ?1
  AND (purge_started_at IS NULL OR purge_started_at < ?2)
RETURNING user_id`, id, now.UTC(), now.Add(-lease).UTC()).Scan(ctx, &owners); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("claim the project's purge: %w", err)
	}
	if len(owners) == 0 {
		return "", false, nil
	}
	return owners[0], true, nil
}

// ClaimProjectPurgeNow claims userID's project for an immediate purge,
// scheduled or not: it is hidden (delete_after, if it was not set) and can no
// longer be restored. It reports false for a project that is missing or
// another user's.
func ClaimProjectPurgeNow(ctx context.Context, db orm.Context, id, userID string, now time.Time) (bool, error) {
	res, err := db.Bun().NewRaw(`UPDATE projects
SET delete_after = COALESCE(delete_after, ?2), deleted_by = COALESCE(deleted_by, ?1), purge_started_at = ?2
WHERE id = ?0 AND user_id = ?1`, id, userID, now.UTC()).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("claim the project's purge: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ListDueProjectPurges lists up to limit projects whose scheduled deletion
// has passed and that no purge holds within lease, soonest first.
func ListDueProjectPurges(ctx context.Context, db orm.Context, now time.Time, lease time.Duration, limit int) ([]string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT id FROM projects
WHERE delete_after IS NOT NULL AND delete_after <= ?0
  AND (purge_started_at IS NULL OR purge_started_at < ?1)
ORDER BY delete_after, id LIMIT ?2`, now.UTC(), now.Add(-lease).UTC(), orm.Arg(limit)).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list the projects due for purge: %w", err)
	}
	return ids, nil
}

// ListDeletedProjects lists the scheduled projects userID owns or deleted
// (as an org admin), whose purge has not begun, soonest purge first: those of
// org's organization, or every one when org is nil. It is a visibility
// predicate; the caller decides each row (authz.ProjectDelete).
func ListDeletedProjects(ctx context.Context, db orm.Context, userID string, org *core.OrgScope) ([]*core.Project, error) {
	var rows []*Project
	q := db.Bun().NewSelect().Model(&rows).
		Where("delete_after IS NOT NULL").
		Where("purge_started_at IS NULL").
		Where("(user_id = ? OR deleted_by = ?)", userID, userID).
		OrderExpr("delete_after ASC, id ASC").
		Limit(500)
	if org != nil {
		whereOrg(ProjectFieldOrgID, org)(q)
	}
	if err := q.Scan(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("list deleted projects: %w", err)
	}
	out := make([]*core.Project, len(rows))
	for i, row := range rows {
		out[i] = projectToCore(row)
	}
	return out, nil
}

// purgeProjectOrphanSteps remove the rows that name a project with no
// foreign key to it, so the project row's cascade never reaches them: a
// worktree recovery deliberately outlives its worktree (its source must never
// erase it), but not the project, and a hosted HTTP action ticket names the
// project beside its workflow. ?0 is the project's id.
var purgeProjectOrphanSteps = []struct{ name, sql string }{
	{"worktree_recovery_checkouts", `
		DELETE FROM worktree_recovery_checkouts
		WHERE recovery_id IN (SELECT id FROM worktree_recoveries WHERE project_id = ?0)`},
	{"worktree_recoveries", `DELETE FROM worktree_recoveries WHERE project_id = ?0`},
	{"hosted_http_action_tickets", `DELETE FROM hosted_http_action_tickets WHERE project_id = ?0`},
}

// purgeProjectOrphans removes the project's rows that no foreign key
// cascades, in one batch transaction.
func purgeProjectOrphans(ctx context.Context, db orm.Context, projectID string) error {
	return projectDeleteBatch(ctx, db, func(ctx context.Context) error {
		for _, step := range purgeProjectOrphanSteps {
			if _, err := db.Bun().NewRaw(step.sql, projectID).Exec(ctx); err != nil {
				return fmt.Errorf("%s: %w", step.name, err)
			}
		}
		return nil
	})
}

// sweepOrphanSteps remove up to ?0 rows of a table keyed by project_id with
// no foreign key whose project no longer exists: those left by deletes from
// before the purge cleaned them (and by an account purge). Each statement is
// one batch.
var sweepOrphanSteps = []struct{ name, sql string }{
	{"worktree_recoveries", `
		WITH gone AS (
			SELECT r.id FROM worktree_recoveries r
			WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = r.project_id)
			LIMIT ?0),
		checkouts AS (
			DELETE FROM worktree_recovery_checkouts WHERE recovery_id IN (SELECT id FROM gone))
		DELETE FROM worktree_recoveries WHERE id IN (SELECT id FROM gone)`},
	{"hosted_http_action_tickets", `
		DELETE FROM hosted_http_action_tickets WHERE id IN (
			SELECT t.id FROM hosted_http_action_tickets t
			WHERE NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = t.project_id)
			LIMIT ?0)`},
}

// SweepOrphanedProjectRows removes up to batch rows per table that name a
// project that no longer exists, and reports how many it removed.
func SweepOrphanedProjectRows(ctx context.Context, db orm.Context, batch int) (int64, error) {
	var total int64
	for _, step := range sweepOrphanSteps {
		err := projectDeleteBatch(ctx, db, func(ctx context.Context) error {
			res, err := db.Bun().NewRaw(step.sql, orm.Arg(batch)).Exec(ctx)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			total += n
			return err
		})
		if err != nil {
			return total, fmt.Errorf("sweep orphaned %s: %w", step.name, err)
		}
	}
	return total, nil
}
