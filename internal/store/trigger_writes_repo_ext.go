// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// EnableTrigger turns a trigger on or off for its owner: the person's pause
// or resume, recorded as "pause" or "enable" in the change history. Turning it
// on locks its project first (refused while the project is scheduled for
// deletion), then the trigger's row; an approval-pinned revision that no
// longer matches is refused. It runs in one transaction, which it opens, or
// joins the one ctx carries.
func EnableTrigger(ctx context.Context, db orm.Context, id string, enabled bool) error {
	if id == "" {
		return fmt.Errorf("trigger ID cannot be empty")
	}
	return db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if enabled {
			current, err := TriggerProjectID(ctx, db, id)
			if err != nil {
				return err
			}
			if err := LockProjectsForWrite(ctx, db, current, ""); err != nil {
				return err
			}
		}
		if err := LockTrigger(ctx, db, id); err != nil {
			return err
		}
		before, err := GetTrigger(ctx, db, id)
		if err != nil {
			return err
		}
		if revision, ok := automationpolicy.Revision(ctx); ok && !before.UpdatedAt.Equal(revision) {
			return svcerr.FailedPrecondition("Activation changed after approval; request a new approval")
		}
		if err := SetTriggerEnabled(ctx, db, id, enabled); err != nil {
			return err
		}
		after, err := GetTrigger(ctx, db, id)
		if err != nil {
			return err
		}
		op := "pause"
		if enabled {
			op = "enable"
		}
		return RecordTriggerChange(ctx, db, op, before, after)
	})
}

// TriggerChanges reads a bounded newest-first configuration history, owner scoped.
func TriggerChanges(ctx context.Context, db orm.Context, userID, triggerID string) ([]core.TriggerChange, error) {
	var rows []struct {
		ID               string
		UserID           string
		ChatID           string
		RunID            string
		ToolCallID       string
		Operation        string
		BeforeState      string
		AfterState       string
		Approval         string
		CapabilityChange string
		CreatedAt        time.Time
	}
	err := db.Bun().NewRaw(`SELECT id,user_id,chat_id,run_id,tool_call_id,operation,COALESCE(before_state::text,'') AS before_state,after_state::text AS after_state,approval,COALESCE(capability_change::text,'') AS capability_change,created_at FROM trigger_changes WHERE user_id=? AND trigger_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, userID, triggerID).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	out := make([]core.TriggerChange, 0, len(rows))
	for _, row := range rows {
		change := core.TriggerChange{ID: row.ID, UserID: row.UserID, ChatID: row.ChatID, RunID: row.RunID, ToolCallID: row.ToolCallID, Operation: row.Operation, BeforeState: row.BeforeState, AfterState: row.AfterState, Approval: row.Approval, CreatedAt: row.CreatedAt}
		if row.CapabilityChange != "" {
			var moved core.CapabilityChange
			if json.Unmarshal([]byte(row.CapabilityChange), &moved) == nil {
				change.CapabilitiesAdded, change.CapabilitiesRemoved = moved.Added, moved.Removed
			}
		}
		out = append(out, change)
	}
	return out, nil
}

// LockActivationDependencies serializes existing mutable dependency edits with
// approval verification and the row mutation. Predicates are also read under
// SERIALIZABLE: insertion/shadowing conflicts cannot produce a mixed snapshot.
// Lock ordering is fixed. Only user/project-scoped rows, never table locks.
func LockActivationDependencies(ctx context.Context, db orm.Context, user, project string) error {
	for _, query := range []string{
		`SELECT id FROM projects WHERE id=?1 AND user_id=?0 FOR SHARE`,
		`SELECT id FROM project_configs WHERE project_id=?1 FOR SHARE`,
		`SELECT id FROM workflow_drafts WHERE user_id=?0 ORDER BY id FOR SHARE`,
		`SELECT id FROM presets WHERE user_id=?0 AND (project_id IS NULL OR project_id=?1) ORDER BY id FOR SHARE`,
		`SELECT id FROM connections WHERE user_id=?0 ORDER BY id FOR SHARE`,
		// Tool bindings are in the approval's fingerprint, and the
		// automation-approvals setting decides whether it needs a card.
		`SELECT id FROM settings WHERE user_id=?0 AND (key LIKE 'tool.bindings.%' OR key = 'automation.approvals') ORDER BY id FOR SHARE`,
		`SELECT id FROM daemons WHERE user_id=?0 ORDER BY id FOR SHARE`,
		`SELECT project_id FROM project_daemons WHERE project_id=?1 ORDER BY daemon_id FOR SHARE`,
	} {
		var ids []string
		if err := db.Bun().NewRaw(query, user, project).Scan(ctx, &ids); err != nil {
			return err
		}
	}
	return nil
}
