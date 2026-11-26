// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The admin takeover of a departed member's automations
// (control-plane docs/design/org-sharing.md §8): the org-wide reads an admin
// lists orphans from, the in-place adoption of a trigger, and the offers that
// adoption by another member goes through (table trigger_offers).

// ListOrgTriggers lists every trigger of one org, whoever owns it, newest
// first, with their display names. Only the takeover's admin read uses it:
// every other read is scoped to one owner.
func ListOrgTriggers(ctx context.Context, db orm.Context, orgID string) ([]*core.Trigger, error) {
	var rows []namedTrigger
	if err := db.Bun().NewRaw(namedTriggerSQL+`WHERE t.org_id = ?0
ORDER BY t.created_at DESC, t.id`, orgID).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list the org's triggers: %w", err)
	}
	out := make([]*core.Trigger, 0, len(rows))
	for i := range rows {
		t, err := rows[i].toCore()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ListOrgWorkflowDrafts lists every saved workflow of one org, whoever owns
// it, most recently updated first.
func ListOrgWorkflowDrafts(ctx context.Context, db orm.Context, orgID string) ([]*core.WorkflowDraft, error) {
	var rows []*WorkflowDraft
	if err := db.Bun().NewSelect().Model(&rows).
		Where("org_id = ?", orgID).
		Order("updated_at DESC").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("list the org's saved workflows: %w", err)
	}
	out := make([]*core.WorkflowDraft, len(rows))
	for i, row := range rows {
		out[i] = workflowDraftToCore(row)
	}
	return out, nil
}

// adoptedTriggerColumns are what an adoption rewrites: the whole definition,
// the owner, and every credential the old owner held. The webhook token hash
// and sealed HMAC secret are cleared (NULL), so the old token stops opening
// the trigger at once and the old secret, sealed under the old owner's
// vault tenant, is gone; the new owner is issued their own. The unattended
// funding stamp is the adopter's, re-stamped as if they created it.
var adoptedTriggerColumns = append(append([]string(nil), triggerDefinitionColumns...),
	TriggerFieldUserID, TriggerFieldPausedReason, TriggerFieldWebhookTokenHash, TriggerFieldWebhookSecretSealed,
	TriggerFieldUnattendedFunding)

// AdoptTrigger re-owns a trigger in place: t is its new row (t.UserID the new
// owner, disabled). Its id, org, kind and history are kept. The caller holds
// the new owner's org lock and the trigger's row lock, and has checked that
// the owner it decided on still owns it.
func AdoptTrigger(ctx context.Context, db orm.Context, t *core.Trigger) error {
	row, err := triggerFromCore(t)
	if err != nil {
		return err
	}
	row.WebhookTokenHash, row.WebhookSecretSealed, row.PausedReason = nil, nil, nil
	err = UpdateTriggerMasked(ctx, db, row, adoptedTriggerColumns, crud.NoReadBack())
	if err != nil {
		// Adopted into a project scheduled for deletion: refused
		// (project_schedule.go).
		return projectDeletedError(err)
	}
	return nil
}

// triggerOfferColumns are trigger_offers' columns, in triggerOfferRow order.
const triggerOfferColumns = `id, trigger_id, org_id, previous_owner_user_id, offered_by_user_id, offeree_user_id,
	status, status_reason, created_at, expires_at, settled_at`

type triggerOfferRow struct {
	ID                  string     `bun:"id"`
	TriggerID           string     `bun:"trigger_id"`
	OrgID               string     `bun:"org_id"`
	PreviousOwnerUserID string     `bun:"previous_owner_user_id"`
	OfferedByUserID     string     `bun:"offered_by_user_id"`
	OffereeUserID       string     `bun:"offeree_user_id"`
	Status              string     `bun:"status"`
	StatusReason        string     `bun:"status_reason"`
	CreatedAt           time.Time  `bun:"created_at"`
	ExpiresAt           time.Time  `bun:"expires_at"`
	SettledAt           *time.Time `bun:"settled_at"`
}

func (r *triggerOfferRow) toCore() *core.TriggerOffer {
	o := core.TriggerOffer(*r)
	o.CreatedAt, o.ExpiresAt = o.CreatedAt.UTC(), o.ExpiresAt.UTC()
	return &o
}

// InsertTriggerOffer stores a pending offer, first revoking (superseded) any
// offer of the same trigger still pending: a trigger has at most one live
// offer (trigger_offers_one_pending). Call it inside a transaction.
func InsertTriggerOffer(ctx context.Context, db orm.Context, o *core.TriggerOffer) error {
	if _, err := db.Bun().NewRaw(`UPDATE trigger_offers
SET status = 'revoked', status_reason = ?, settled_at = ?
WHERE trigger_id = ? AND status = 'pending'`, core.TriggerOfferRevokedSuperseded, o.CreatedAt, o.TriggerID).Exec(ctx); err != nil {
		return fmt.Errorf("supersede the trigger's pending offer: %w", err)
	}
	_, err := db.Bun().NewRaw(`INSERT INTO trigger_offers (`+triggerOfferColumns+`)
VALUES (?, ?, ?, ?, ?, ?, 'pending', '', ?, ?, NULL)`,
		o.ID, o.TriggerID, o.OrgID, o.PreviousOwnerUserID, o.OfferedByUserID, o.OffereeUserID, o.CreatedAt, o.ExpiresAt).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record trigger offer: %w", err)
	}
	return nil
}

// GetTriggerOffer returns one offer, or core.ErrTriggerOfferNotFound.
func GetTriggerOffer(ctx context.Context, db orm.Context, id string) (*core.TriggerOffer, error) {
	var row triggerOfferRow
	err := db.Bun().NewRaw(`SELECT `+triggerOfferColumns+` FROM trigger_offers WHERE id = ?`, id).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrTriggerOfferNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get trigger offer: %w", err)
	}
	return row.toCore(), nil
}

// ListPendingTriggerOffers lists the pending offers f selects, newest first.
// Expiry is not applied here: the caller decides whether each is live.
func ListPendingTriggerOffers(ctx context.Context, db orm.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error) {
	q := db.Bun().NewSelect().TableExpr("trigger_offers").ColumnExpr(triggerOfferColumns).
		Where("status = 'pending'")
	if f.OffereeUserID != "" {
		q = q.Where("offeree_user_id = ?", f.OffereeUserID)
	}
	if f.OrgID != nil {
		q = q.Where("org_id = ?", *f.OrgID)
	}
	if f.TriggerIDs != nil {
		if len(f.TriggerIDs) == 0 {
			return nil, nil
		}
		q = q.Where("trigger_id = ANY(?::text[])", pgdialect.Array(f.TriggerIDs))
	}
	var rows []triggerOfferRow
	if err := q.OrderExpr("created_at DESC, id").Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list pending trigger offers: %w", err)
	}
	out := make([]*core.TriggerOffer, len(rows))
	for i := range rows {
		out[i] = rows[i].toCore()
	}
	return out, nil
}

// SettleTriggerOffer moves a pending offer to status (accepted, revoked or
// expired) with reason. settled is false when it was no longer pending.
func SettleTriggerOffer(ctx context.Context, db orm.Context, id, status, reason string, at time.Time) (settled bool, err error) {
	res, err := db.Bun().NewRaw(`UPDATE trigger_offers SET status = ?, status_reason = ?, settled_at = ?
WHERE id = ? AND status = 'pending'`, status, reason, at, id).Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("settle trigger offer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// RevokeOtherTriggerOffers revokes every pending offer of a trigger except
// keep: the trigger was adopted, so no other offer of it can be accepted.
func RevokeOtherTriggerOffers(ctx context.Context, db orm.Context, triggerID, keep string, at time.Time) error {
	_, err := db.Bun().NewRaw(`UPDATE trigger_offers SET status = 'revoked', status_reason = ?, settled_at = ?
WHERE trigger_id = ? AND status = 'pending' AND id <> ?`, core.TriggerOfferRevokedOwnerChanged, at, triggerID, keep).Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke the trigger's other offers: %w", err)
	}
	return nil
}

// LockUserOrgs takes userID's org lock for the rest of the surrounding
// transaction, then checks each read: its row still has the org the decision
// read. It must run inside a transaction (the lock is transaction-scoped)
// and before any row lock of that transaction.
func LockUserOrgs(ctx context.Context, db orm.Context, userID string, reads ...OrgRead) error {
	if userID == "" {
		return fmt.Errorf("org lock: user is required")
	}
	return lockUserOrgs(ctx, db, userOrgLockSQL, userID, reads)
}

// lockUserOrgs takes the org lock with lockSQL (exclusive or shared), then
// re-checks each read.
func lockUserOrgs(ctx context.Context, db orm.Context, lockSQL, userID string, reads []OrgRead) error {
	if _, err := db.Bun().NewRaw(lockSQL, userID).Exec(ctx); err != nil {
		return fmt.Errorf("take the org lock: %w", err)
	}
	for _, read := range reads {
		query, ok := orgReadSQL[read.Table]
		if !ok {
			return fmt.Errorf("org lock: %q has no org", read.Table)
		}
		var rows []struct {
			OrgID *string `bun:"org_id"`
		}
		if err := db.Bun().NewRaw(query, read.ID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("re-read the org of %s %s: %w", read.Table, read.ID, err)
		}
		if len(rows) == 0 || !sameOrg(rows[0].OrgID, read.Org) {
			return ErrOrgChanged
		}
	}
	return nil
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

// RecordTriggerChange is called in the SAME transaction as the row mutation.
// Only the bounded allowlisted projection is persisted: arbitrary params,
// prompts, source expressions, and graph literals are never audit data.
func RecordTriggerChange(ctx context.Context, db orm.Context, op string, before, after *core.Trigger) error {
	var old any
	if before != nil {
		data, err := automationpolicy.SafeJSON(before)
		if err != nil {
			return err
		}
		old = string(data)
	}
	data, err := automationpolicy.SafeJSON(after)
	if err != nil {
		return err
	}
	actor := automationpolicy.ActorFrom(ctx)
	// How an agent's change was approved, and the capabilities it moved
	// (automationpolicy.Decide). Capability items are display-safe by
	// construction; a change made in the app records neither.
	approval := automationpolicy.ApprovalFrom(ctx)
	var capabilityChange any
	if approval.How != "" {
		raw, err := json.Marshal(core.CapabilityChange{Added: nonNil(approval.Added), Removed: nonNil(approval.Removed)})
		if err != nil {
			return err
		}
		capabilityChange = string(raw)
	}
	_, err = db.Bun().NewRaw(`INSERT INTO trigger_changes(id,trigger_id,user_id,chat_id,run_id,tool_call_id,operation,before_state,after_state,approval,capability_change,created_at) VALUES (?,?,?,?,?,?,?,?::jsonb,?::jsonb,?,?::jsonb,?)`, uuid.NewString(), after.ID, after.UserID, actor.ChatID, actor.RunID, actor.ToolCallID, op, old, string(data), approval.How, capabilityChange, time.Now().UTC()).Exec(ctx)
	if err != nil {
		return fmt.Errorf("record trigger configuration change: %w", err)
	}
	return nil
}
