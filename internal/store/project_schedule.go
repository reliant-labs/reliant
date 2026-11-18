// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

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
// Scheduling takes the project row FOR UPDATE first. Every write that would
// add to the project (a chat, worktree or run, or a trigger created, moved in
// or turned on) takes a key share on the row and reads delete_after under it
// (the project_delete_admission triggers, migration
// 20261118103211_project_delete_write_admission). The two conflict, so a
// write either commits before the schedule scans the project's triggers (and
// a trigger it left on is paused with the rest), or waits for the schedule
// and is refused (core.ErrProjectScheduledForDeletion). Restoring takes the
// row FOR NO KEY UPDATE: it waits for a schedule, and the triggers it turns
// on read the delete_after it cleared.
//
// Lock order, the project move's (MoveProjectOrg) and the trigger writers'
// (LockProjectsForWrite) alike: the owner's org lock, then the project row,
// then any trigger row. Schedule, restore and an immediate purge's claim take
// the owner's org lock (lockProjectDecision) and re-check the owner and org
// the caller's authority was decided on, so a move that committed between the
// decision and the write refuses it (ErrOrgChanged).

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

// lockProjectDecision takes the project owner's org lock (LockUserOrgs: the
// lock a project move takes first) and re-checks, under it, that the project
// is still decided's owner's and in decided's org. A move, or anything else
// that changed either, since the caller decided is ErrOrgChanged. It must be
// the first lock of the caller's transaction.
func lockProjectDecision(ctx context.Context, db orm.Context, id string, decided core.ProjectDecision) error {
	owner := func() (string, bool, error) {
		var owners []string
		if err := db.Bun().NewRaw(`SELECT user_id FROM projects WHERE id = ?0`, id).Scan(ctx, &owners); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", false, fmt.Errorf("read the project's owner: %w", err)
		}
		if len(owners) == 0 {
			return "", false, nil
		}
		return owners[0], true, nil
	}
	// A project that is gone (purged) is NotFound, not a changed org. The
	// read takes no lock, so it may precede the org lock.
	if _, found, err := owner(); err != nil {
		return err
	} else if !found {
		return core.ErrProjectNotFound
	}
	if err := LockUserOrgs(ctx, db, decided.OwnerUserID, OrgRead{Table: "projects", ID: id, Org: decided.OrgID}); err != nil {
		return err
	}
	current, found, err := owner()
	switch {
	case err != nil:
		return err
	case !found:
		return core.ErrProjectNotFound
	case current != decided.OwnerUserID:
		return ErrOrgChanged
	}
	return nil
}

// ScheduleProjectDeletion schedules the project for purge at deleteAfter,
// recording by as who did, and pauses its enabled triggers. A project already
// scheduled keeps its first date (AlreadyScheduled); any of its triggers found
// on (turned on by a writer that bypassed the admission, which no release
// has) is paused again. A missing project is core.ErrProjectNotFound; one
// whose owner or org is no longer decided's is ErrOrgChanged
// (lockProjectDecision). Run it in a transaction, first.
func ScheduleProjectDeletion(ctx context.Context, db orm.Context, id, by string, deleteAfter time.Time, decided core.ProjectDecision) (core.ProjectDeletion, error) {
	if err := lockProjectDecision(ctx, db, id, decided); err != nil {
		return core.ProjectDeletion{}, err
	}
	var rows []struct {
		DeleteAfter *time.Time `bun:"delete_after"`
	}
	// FOR UPDATE, not FOR NO KEY UPDATE: it must conflict with the key share
	// each admitted write holds on the row (see the top of this file).
	if err := db.Bun().NewRaw(`SELECT delete_after FROM projects WHERE id = ?0 FOR UPDATE`, id).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return core.ProjectDeletion{}, fmt.Errorf("lock the project: %w", err)
	}
	if len(rows) == 0 {
		return core.ProjectDeletion{}, core.ErrProjectNotFound
	}
	if rows[0].DeleteAfter != nil {
		paused, err := pauseProjectTriggers(ctx, db, id)
		if err != nil {
			return core.ProjectDeletion{}, err
		}
		return core.ProjectDeletion{DeleteAfter: rows[0].DeleteAfter.UTC(), AlreadyScheduled: true, PausedTriggerIDs: paused}, nil
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
// core.ErrProjectPurgeStarted; a missing one core.ErrProjectNotFound; one
// whose owner or org is no longer decided's ErrOrgChanged
// (lockProjectDecision). Run it in a transaction, first.
func RestoreProjectDeletion(ctx context.Context, db orm.Context, id string, decided core.ProjectDecision) ([]string, error) {
	if err := lockProjectDecision(ctx, db, id, decided); err != nil {
		return nil, err
	}
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

// projectDeletedConstraint is the constraint name the project_delete_admission
// triggers raise with (SQLSTATE 55000).
const projectDeletedConstraint = "project_scheduled_for_deletion"

// projectDeletedError maps the database's refusal of a write to a project
// scheduled for deletion to core.ErrProjectScheduledForDeletion.
func projectDeletedError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55000" && pg.ConstraintName == projectDeletedConstraint {
		return core.ErrProjectScheduledForDeletion
	}
	return err
}

// LockProjectsForWrite takes a key share on each project in ids (in id
// order; "" is skipped) and refuses with core.ErrProjectScheduledForDeletion
// when one is scheduled for deletion. A trigger write takes it before its
// trigger's row lock: the database's admission takes the same share later in
// the statement, and taking it after the trigger lock would order the two
// locks against a schedule's (project, then triggers). A project that is gone
// is left to the write's own foreign key.
func LockProjectsForWrite(ctx context.Context, db orm.Context, ids ...string) error {
	var want []string
	for _, id := range ids {
		if id != "" {
			want = append(want, id)
		}
	}
	if len(want) == 0 {
		return nil
	}
	var scheduled []string
	if err := db.Bun().NewRaw(`SELECT id FROM (SELECT id, delete_after FROM projects WHERE id = ANY(?0) ORDER BY id FOR KEY SHARE) p
WHERE delete_after IS NOT NULL`, pgdialect.Array(want)).Scan(ctx, &scheduled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock the project: %w", err)
	}
	if len(scheduled) > 0 {
		return core.ErrProjectScheduledForDeletion
	}
	return nil
}

// TriggerProjectID reads the trigger's project, unlocked: what a trigger
// write locks (LockProjectsForWrite) before the trigger's own row. "" for a
// trigger that is gone (its write then finds it missing).
func TriggerProjectID(ctx context.Context, db orm.Context, triggerID string) (string, error) {
	var ids []string
	if err := db.Bun().NewRaw(`SELECT project_id FROM triggers WHERE id = ?0`, triggerID).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read the trigger's project: %w", err)
	}
	if len(ids) == 0 {
		return "", nil
	}
	return ids[0], nil
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
// scheduled or not: it is hidden and can no longer be restored, and its
// delete_after is brought forward to now (LEAST ignores a NULL), so a purge
// that stops part way is resumed by the worker once the claim's lease runs
// out, not at the end of a restore window nobody can use any more. The
// owner purges: decided.OwnerUserID. A project that is missing is
// core.ErrProjectNotFound, one whose owner or org is no longer decided's
// ErrOrgChanged (lockProjectDecision). Run it in a transaction, first.
func ClaimProjectPurgeNow(ctx context.Context, db orm.Context, id string, decided core.ProjectDecision, now time.Time) (bool, error) {
	if err := lockProjectDecision(ctx, db, id, decided); err != nil {
		return false, err
	}
	res, err := db.Bun().NewRaw(`UPDATE projects
SET delete_after = LEAST(delete_after, ?2), deleted_by = COALESCE(deleted_by, ?1), purge_started_at = ?2
WHERE id = ?0 AND user_id = ?1`, id, decided.OwnerUserID, now.UTC()).Exec(ctx)
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
// org's organization, or every one when org is nil. includePurging also lists
// userID's own projects whose purge has begun (only PurgeProjectNow can act on
// those). It is a visibility predicate; the caller decides each row
// (authz.ProjectDelete).
func ListDeletedProjects(ctx context.Context, db orm.Context, userID string, org *core.OrgScope, includePurging bool) ([]*core.Project, error) {
	var rows []*Project
	q := db.Bun().NewSelect().Model(&rows).
		Where("delete_after IS NOT NULL").
		Where("(purge_started_at IS NULL OR (? AND user_id = ?))", includePurging, userID).
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

// projectScheduledForDeletion reports whether projectID is scheduled for
// deletion: a by-id chat read marks the chat with it (core.Chat.
// ProjectDeleted), so internal/authz hides a deleted project's chats from
// whoever holds their ids. A project that is gone reads as not scheduled;
// its chats go with it.
func projectScheduledForDeletion(ctx context.Context, db orm.Context, projectID string) (bool, error) {
	if projectID == "" {
		return false, nil
	}
	var scheduled []bool
	if err := db.Bun().NewRaw(`SELECT delete_after IS NOT NULL FROM projects WHERE id = ?0`, projectID).
		Scan(ctx, &scheduled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read the chat's project: %w", err)
	}
	return len(scheduled) == 1 && scheduled[0], nil
}

// withProjectDeleted marks chat with its project's deletion state.
func withProjectDeleted(ctx context.Context, db orm.Context, chat *core.Chat) (*core.Chat, error) {
	deleted, err := projectScheduledForDeletion(ctx, db, chat.ProjectID)
	if err != nil {
		return nil, err
	}
	chat.ProjectDeleted = deleted
	return chat, nil
}
