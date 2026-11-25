// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The org-checked writes of a trigger's definition: the insert and the
// replace each run under the owner's org lock, the project locks and the
// trigger's row lock (org_lock.go), and record the change in the same
// transaction. No entity owns this file (it spans triggers, projects and
// trigger_changes), so forge projects TriggerWritesExtStore from it.

// mapTriggerNameConflict turns a unique violation on the trigger's
// (user, project, name) key into core.ErrTriggerNameTaken: AlreadyExists to
// the caller instead of an opaque Internal "database error". The PgError is
// not wrapped (%w), so a retry ladder does not re-run a permanent conflict.
func mapTriggerNameConflict(err error, name string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == TriggerConstraintUserIDProjectIDNameKey {
		return fmt.Errorf("%w: %q", core.ErrTriggerNameTaken, name)
	}
	return err
}

// CreateTriggerInOrg inserts t, under its owner's org lock, only while each
// read (the org t's was decided from: its project's) still holds. It must run
// inside a transaction: the locks are transaction-scoped.
func CreateTriggerInOrg(ctx context.Context, db orm.Context, t *core.Trigger, reads ...OrgRead) error {
	if t == nil {
		return fmt.Errorf("trigger cannot be nil")
	}
	if err := LockUserOrgs(ctx, db, t.UserID, reads...); err != nil {
		return err
	}
	// Then its project: refused while scheduled for deletion, and a schedule
	// waits for this insert (LockProjectsForWrite).
	if err := LockProjectsForWrite(ctx, db, t.ProjectID); err != nil {
		return err
	}
	if err := InsertTrigger(ctx, db, t); err != nil {
		return mapTriggerNameConflict(err, t.Name)
	}
	after, err := GetTrigger(ctx, db, t.ID)
	if err != nil {
		return err
	}
	return RecordTriggerChange(ctx, db, "create", nil, after)
}

// UpdateTriggerInOrg replaces the trigger's definition under its owner's org
// lock, only while each read still holds: the orgs the update's decision read
// (the trigger's, its project's, the target project's). A backfill that
// stamped any of them between the decision and this write makes it
// ErrOrgChanged, and nothing is written; a NULL org read is compared like any
// other. It must run inside a transaction: the locks are transaction-scoped.
func UpdateTriggerInOrg(ctx context.Context, db orm.Context, t *core.Trigger, reads ...OrgRead) error {
	if t == nil {
		return fmt.Errorf("trigger cannot be nil")
	}
	if t.ID == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	// The org lock first, then the projects the trigger is in and moves to,
	// then the trigger's row (org_lock.go, LockProjectsForWrite). A trigger
	// left on, or moved, in a project scheduled for deletion is refused.
	if err := LockUserOrgs(ctx, db, t.UserID, reads...); err != nil {
		return err
	}
	current, err := TriggerProjectID(ctx, db, t.ID)
	if err != nil {
		return err
	}
	if t.Enabled || (t.ProjectID != "" && t.ProjectID != current) {
		if err := LockProjectsForWrite(ctx, db, current, t.ProjectID); err != nil {
			return err
		}
	}
	if err := LockTrigger(ctx, db, t.ID); err != nil {
		return err
	}
	before, err := GetTrigger(ctx, db, t.ID)
	if err != nil {
		return err
	}
	if revision, ok := automationpolicy.Revision(ctx); ok && !before.UpdatedAt.Equal(revision) {
		return svcerr.FailedPrecondition("Activation changed after approval; request a new approval")
	}
	if err := ReplaceTrigger(ctx, db, t); err != nil {
		return mapTriggerNameConflict(err, t.Name)
	}
	after, err := GetTrigger(ctx, db, t.ID)
	if err != nil {
		return err
	}
	return RecordTriggerChange(ctx, db, "update", before, after)
}

// SystemPauseTrigger is the system pausing a trigger that can no longer launch
// as written, recording reason (core.TriggerPaused*) on the row and the pause
// in its change history, in one transaction. paused is false when the trigger
// was already paused (a person did, or a concurrent fire got here first):
// nothing is written, so a burst of refused fires records one pause. It opens
// its own transaction, or joins the one ctx carries.
func SystemPauseTrigger(ctx context.Context, db orm.Context, id, reason string) (paused bool, err error) {
	if id == "" {
		return false, fmt.Errorf("trigger ID cannot be empty")
	}
	err = db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if err := LockTrigger(ctx, db, id); err != nil {
			return err
		}
		before, err := GetTrigger(ctx, db, id)
		if err != nil {
			return err
		}
		if !before.Enabled {
			return nil
		}
		if err := PauseTrigger(ctx, db, id, reason); err != nil {
			return err
		}
		after, err := GetTrigger(ctx, db, id)
		if err != nil {
			return err
		}
		paused = true
		return RecordTriggerChange(ctx, db, "pause", before, after)
	})
	if err != nil {
		return false, err
	}
	return paused, nil
}
