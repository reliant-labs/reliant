// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// This is the custom-query seam for Approval. The generated approval_orm.go is
// Tier-1 (a live projection of the migrations, regenerated every run);
// hand-written queries that outgrow the generated CRUD belong HERE, in a
// sibling file forge never rewrites. You have three building blocks:
//
//  1. The generated repo + delegates: approvalRepo (a *crud.Repo[Approval]) and the
//     package-level GetApprovalByID / ListApproval / CreateApproval / UpdateApproval / DeleteApproval
//     functions — compose orm.QueryOption filters (orm.WhereEq,
//     orm.WhereILikeAny, orm.WithOrderBy, orm.WithLimit) onto List/Count.
//  2. Raw SQL via the Bun handle: db.Bun() (bun.IDB) gives you
//     NewSelect()/NewRaw()/bun.Raw — for joins, aggregates, CTEs, or
//     anything the builder can't express. Scan into the generated
//     Approval struct (or your own row type).
//  3. orm.Context: every query takes one. Take no transaction parameter:
//     called with the ctx that s.deps.DB.RunTx hands its fn, a method
//     here (db.Bun() builders and raw SQL included) joins that
//     transaction; with any other ctx it runs on the pool.
//
// Example (uncomment and adapt):
//
//	func FindApprovalBy(ctx context.Context, db orm.Context, /* args */) ([]*Approval, error) {
//		var rows []*Approval
//		err := db.Bun().NewSelect().Model(&rows).
//			Where("chat_id = ?", /* value */).
//			Scan(ctx)
//		return rows, err
//	}
//
// Or reuse the generated repo for a filtered list:
//
//	func ActiveApprovals(ctx context.Context, db orm.Context) ([]*Approval, error) {
//		return ListApproval(ctx, db, orm.WhereEq("chat_id", true))
//	}
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// CreateApprovalIfAbsent records an approval once per entity: a second
// approval for the same entity_id (a content block or a workflow event) is a
// no-op, which is what makes raising one idempotent across activity retries.
func CreateApprovalIfAbsent(ctx context.Context, db orm.Context, a *core.Approval) error {
	_, err := db.Bun().NewInsert().Model(approvalFromCore(a)).
		On("CONFLICT (entity_id) DO NOTHING").
		Exec(ctx)
	return err
}

// GetApproval returns the approval, or (nil, nil) when there is none.
func GetApproval(ctx context.Context, db orm.Context, id string) (*core.Approval, error) {
	row, err := GetApprovalByID(ctx, db, id)
	if errors.Is(err, svcerr.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return approvalToCore(row), nil
}

// GetApprovalByEntityID returns the approval raised for an entity (a content
// block or a workflow event), or (nil, nil) when there is none. entity_id is
// UNIQUE, so there is at most one.
func GetApprovalByEntityID(ctx context.Context, db orm.Context, entityID string) (*core.Approval, error) {
	rows, err := ListApproval(ctx, db, orm.WhereEq(ApprovalFieldEntityID, entityID), orm.WithLimit(1))
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return approvalToCore(rows[0]), nil
}

// ListPendingApprovalsByChat returns the chat's unresolved approvals, oldest
// first.
func ListPendingApprovalsByChat(ctx context.Context, db orm.Context, chatID string) ([]*core.Approval, error) {
	rows, err := ListApproval(ctx, db,
		orm.WhereEq(ApprovalFieldChatID, chatID),
		orm.WhereEq(ApprovalFieldStatus, reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING),
		orm.WithOrderBy(ApprovalFieldCreatedAt, orm.Asc))
	if err != nil {
		return nil, err
	}
	return approvalsToCore(rows), nil
}

// ListApprovalsByChat returns every approval of the chat, resolved ones
// included, newest first.
func ListApprovalsByChat(ctx context.Context, db orm.Context, chatID string) ([]*core.Approval, error) {
	rows, err := ListApproval(ctx, db,
		orm.WhereEq(ApprovalFieldChatID, chatID),
		orm.WithOrderBy(ApprovalFieldCreatedAt, orm.Desc))
	if err != nil {
		return nil, err
	}
	return approvalsToCore(rows), nil
}

// UpdateApprovalStatus resolves (or re-opens) an approval. denialReason,
// actionTaken and resolvedAt are written as given, nil clearing them;
// metadata is written only when set, so a resolution that carries none keeps
// what the approval was raised with. A missing approval is orm.ErrNoRows.
func UpdateApprovalStatus(ctx context.Context, db orm.Context, id string, status int32, denialReason, actionTaken, metadata *string, resolvedAt *time.Time) error {
	row := &Approval{
		ID:           id,
		Status:       reliantv1.ApprovalStatus(status),
		DenialReason: denialReason,
		ActionTaken:  actionTaken,
		ResolvedAt:   resolvedAt,
		Metadata:     metadata,
	}
	fields := []string{ApprovalFieldStatus, ApprovalFieldDenialReason, ApprovalFieldActionTaken, ApprovalFieldResolvedAt}
	if metadata != nil {
		fields = append(fields, ApprovalFieldMetadata)
	}
	return UpdateApprovalMasked(ctx, db, row, fields)
}

// resolvePendingApprovalSQL closes one approval only while it is still
// pending: a person's decision (or another closer) that got there first is
// kept, and the caller learns it lost from the empty result.
const resolvePendingApprovalSQL = `
UPDATE approvals
SET status = ?1, denial_reason = ?2, action_taken = ?3, resolved_at = ?4
WHERE id = ?0 AND status = 1
RETURNING id, chat_id, approval_type, entity_id, status, denial_reason, title, metadata,
    created_at, resolved_at, action_taken, temporal_workflow_id, thread_id`

// ResolvePendingApproval moves a pending approval to status (a terminal one)
// with reason and actionTaken recorded, and returns the closed row. It
// returns (nil, nil) when the approval is not pending (or not there): nothing
// is overwritten.
func ResolvePendingApproval(ctx context.Context, db orm.Context, id string, status reliantv1.ApprovalStatus, reason, actionTaken string, at time.Time) (*core.Approval, error) {
	var rows []*Approval
	if err := db.Bun().NewRaw(resolvePendingApprovalSQL, id, int32(status), reason, actionTaken, at).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("resolve pending approval %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return approvalToCore(rows[0]), nil
}

// StaleApproval is a pending approval no one can act on any more, and why.
type StaleApproval struct {
	ID     string `bun:"id"`
	ChatID string `bun:"chat_id"`
	// Why is the reason for the person: what ended the wait.
	Why string `bun:"why"`
}

// listStaleApprovalsSQL finds pending approvals whose wait is over: the run
// that asked (the newest run of the approval's thread, as pending_input_gates
// resolves it) or the chat's root run completed (stop_reason 1) or was
// cancelled (4), the chat was archived (state 3), or the chat is gone. A
// failed (2) or paused (3) run is resumable and keeps its approval, the rule
// ListInboxPending lists by. idx_approvals_pending keeps the scan to the
// pending rows.
const listStaleApprovalsSQL = `
SELECT a.id, a.chat_id,
    CASE
        WHEN ch.id IS NULL THEN 'its chat was deleted'
        WHEN ch.state = 3 THEN 'its chat was archived'
        WHEN g.run_stop_reason = 4 OR rw.stop_reason = 4 THEN 'the run waiting on it was cancelled'
        ELSE 'the run waiting on it finished'
    END AS why
FROM approvals a
LEFT JOIN chats ch ON ch.id = a.chat_id
LEFT JOIN workflows rw ON rw.id = ch.workflow_id
LEFT JOIN pending_input_gates g ON g.kind = 'approval' AND g.id = a.id
WHERE a.status = 1
  AND (
      ch.id IS NULL
      OR ch.state = 3
      OR (g.run_state = 3 AND g.run_stop_reason IN (1, 4))
      OR (rw.state = 3 AND rw.stop_reason IN (1, 4))
  )
ORDER BY a.created_at, a.id
LIMIT ?0`

// ListStaleApprovals returns up to limit pending approvals whose wait is over
// (listStaleApprovalsSQL), oldest first.
func ListStaleApprovals(ctx context.Context, db orm.Context, limit int) ([]StaleApproval, error) {
	var out []StaleApproval
	if err := db.Bun().NewRaw(listStaleApprovalsSQL, limit).Scan(ctx, &out); err != nil {
		return nil, fmt.Errorf("list stale approvals: %w", err)
	}
	return out, nil
}

// listUnannouncedResolutionsSQL finds closed approvals whose chat feed still
// says pending: the newest approval update for the row (update_type 2) is a
// "pending" one. Clients rebuild a chat's approvals from that feed (the
// newest update per entity is in every snapshot), so such a row reads as
// actionable forever. Before ApprovalResolve announced what it closed, every
// timed-out approval was one. Each row is one probe of
// idx_chat_updates_snapshot_entity_heads.
const listUnannouncedResolutionsSQL = `
SELECT a.id, a.chat_id, a.approval_type, a.entity_id, a.status, a.denial_reason, a.title, a.metadata,
    a.created_at, a.resolved_at, a.action_taken, a.temporal_workflow_id, a.thread_id
FROM approvals a
WHERE a.status <> 1
  AND (
      SELECT u.data::jsonb ->> 'status' FROM chat_updates u
      WHERE u.chat_id = a.chat_id AND u.entity_id = a.id AND u.update_type = 2
      ORDER BY u.sequence_number DESC
      LIMIT 1
  ) = 'pending'
ORDER BY a.created_at, a.id
LIMIT ?0`

// ListUnannouncedApprovalResolutions returns up to limit closed approvals
// whose chat feed still announces them as pending
// (listUnannouncedResolutionsSQL), oldest first.
func ListUnannouncedApprovalResolutions(ctx context.Context, db orm.Context, limit int) ([]*core.Approval, error) {
	var rows []*Approval
	if err := db.Bun().NewRaw(listUnannouncedResolutionsSQL, limit).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("list unannounced approval resolutions: %w", err)
	}
	return approvalsToCore(rows), nil
}

func approvalFromCore(a *core.Approval) *Approval {
	return &Approval{
		ID:                 a.ID,
		ChatID:             a.ChatID,
		ApprovalType:       reliantv1.ApprovalType(a.ApprovalType),
		EntityID:           a.EntityID,
		Status:             reliantv1.ApprovalStatus(a.Status),
		DenialReason:       a.DenialReason,
		Title:              a.Title,
		Metadata:           a.Metadata,
		CreatedAt:          a.CreatedAt,
		ResolvedAt:         a.ResolvedAt,
		ActionTaken:        a.ActionTaken,
		TemporalWorkflowID: a.TemporalWorkflowID,
		ThreadID:           a.ThreadID,
		BatchID:            a.BatchID,
	}
}

func approvalToCore(row *Approval) *core.Approval {
	return &core.Approval{
		ID:                 row.ID,
		ChatID:             row.ChatID,
		ApprovalType:       int32(row.ApprovalType),
		EntityID:           row.EntityID,
		Status:             int32(row.Status),
		DenialReason:       row.DenialReason,
		ActionTaken:        row.ActionTaken,
		Title:              row.Title,
		Metadata:           row.Metadata,
		TemporalWorkflowID: row.TemporalWorkflowID,
		CreatedAt:          row.CreatedAt,
		ResolvedAt:         row.ResolvedAt,
		ThreadID:           row.ThreadID,
		BatchID:            row.BatchID,
	}
}

func approvalsToCore(rows []*Approval) []*core.Approval {
	out := make([]*core.Approval, len(rows))
	for i, row := range rows {
		out[i] = approvalToCore(row)
	}
	return out
}

// ApprovalExtra carries fields on Approval that are NOT columns — computed
// values, transient state, anything derived rather than stored. Approval is
// embedded on ApprovalExtra (see approval_orm.go), so a field added here is a field on
// every Approval value.
//
// Every field MUST carry the `bun:"-"` tag. Bun otherwise treats an
// embedded struct's fields as columns of the parent table and tries to
// persist them — `bun:"-"` is Bun's own idiom for a field it must never
// bind or scan.
type ApprovalExtra struct {
}
