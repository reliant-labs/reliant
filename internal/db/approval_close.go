// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

// AN APPROVAL NOBODY CAN ANSWER IS NOT PENDING.
//
// An approval is a wait in a run: the run parks on a signal named for the
// approval and the person's Approve or Deny sends it. When the wait ends
// without them — its timer fires, the run is interrupted, cancelled or
// finishes, the chat is archived — the row must leave PENDING, and the chat
// feed must say so: clients rebuild a chat's approvals from that feed (the
// newest update per approval is in every snapshot), so a row closed without
// an announcement keeps an Approve button on screen that can only fail. That
// is what timed-out approvals did until ResolvePendingApproval announced them.
//
// Every system close goes through ResolvePendingApproval: guarded by
// status = pending (a decision that got there first is kept), recorded with
// its reason (denial_reason) and what closed it (action_taken), and announced
// in the same transaction.

// ApprovalFeedStatus is the status string a chat feed approval update carries
// for status. Clients map it back; an unknown one must never read as pending.
func ApprovalFeedStatus(status int32) string {
	switch reliantv1.ApprovalStatus(status) {
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING:
		return "pending"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_APPROVED:
		return "approved"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED:
		return "denied"
	case reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED:
		return "expired"
	default:
		return "cancelled"
	}
}

// ResolvePendingApproval closes a pending approval no one can act on any more
// with status (EXPIRED or CANCELLED), recording reason and actionTaken, and
// announces it on its chat's feed in the same transaction. It reports false,
// changing nothing, when the approval is no longer pending.
func (r *Repo) ResolvePendingApproval(ctx context.Context, id string, status int32, reason, actionTaken string) (bool, error) {
	var closed *core.Approval
	err := r.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		closed = nil
		a, err := store.ResolvePendingApproval(ctx, r.ORM(), id, reliantv1.ApprovalStatus(status), reason, actionTaken, time.Now().UTC())
		if err != nil || a == nil {
			return err
		}
		closed = a
		return r.AnnounceApprovalResolution(ctx, a)
	})
	if err != nil {
		return false, err
	}
	if closed == nil {
		return false, nil
	}
	// The chat may stop reading AWAITING_INPUT.
	if err := r.emitChatActivityIfChanged(ctx, closed.ChatID); err != nil {
		logging.WarnContext(ctx, "Failed to announce chat activity after closing an approval", "approvalID", id, "chatID", closed.ChatID, "error", err)
	}
	return true, nil
}

// AttributeApprovalUpdate adds to an approval's chat feed update the workflow
// that raised it (the workflow_id Approval.workflow_id reports) and its thread,
// when known. Every approval update carries them, its resolution included, so
// a client replaying the feed attributes an approval to its run on its own:
// once resolved it is gone from the pending-only ListApprovalsByChat.
func AttributeApprovalUpdate(update map[string]any, a *core.Approval) {
	if a.Metadata != nil {
		var metadata struct {
			WorkflowID string `json:"workflow_id"`
		}
		if err := json.Unmarshal([]byte(*a.Metadata), &metadata); err == nil && metadata.WorkflowID != "" {
			update["workflow_id"] = metadata.WorkflowID
		}
	}
	if a.ThreadID != nil && *a.ThreadID != "" {
		update["thread_id"] = *a.ThreadID
	}
}

// AnnounceApprovalResolution writes the chat feed update that tells clients
// an approval is resolved, with its status, reason and when.
func (r *Repo) AnnounceApprovalResolution(ctx context.Context, a *core.Approval) error {
	status := ApprovalFeedStatus(a.Status)
	if a.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED) && a.ActionTaken != nil && *a.ActionTaken == "timeout" {
		// Timed out before EXPIRED existed: nobody denied it.
		status = ApprovalFeedStatus(int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED))
	}
	update := map[string]any{
		"update_type":   "approval",
		"id":            a.ID,
		"approval_type": a.ApprovalType,
		"entity_id":     a.EntityID,
		"status":        status,
		"title":         a.Title,
	}
	if a.DenialReason != nil {
		update["denial_reason"] = *a.DenialReason
	}
	if a.ActionTaken != nil {
		update["action_taken"] = *a.ActionTaken
	}
	if a.ResolvedAt != nil {
		update["resolved_at"] = a.ResolvedAt.UTC().Format(time.RFC3339)
	}
	AttributeApprovalUpdate(update, a)
	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal approval resolution: %w", err)
	}
	return r.CreateChatUpdate(ctx, a.ChatID, UpdateTypeApproval, a.ID, string(data))
}

// StaleApproval is a pending approval whose wait is over, and why.
type StaleApproval = store.StaleApproval

// ListStaleApprovals returns up to limit pending approvals whose wait is over
// (store.ListStaleApprovals), each with the reason for the person.
func (r *Repo) ListStaleApprovals(ctx context.Context, limit int) ([]StaleApproval, error) {
	return store.ListStaleApprovals(ctx, r.ORM(), limit)
}

// ListUnannouncedApprovalResolutions returns up to limit closed approvals
// whose chat feed still announces them as pending.
func (r *Repo) ListUnannouncedApprovalResolutions(ctx context.Context, limit int) ([]*Approval, error) {
	return store.ListUnannouncedApprovalResolutions(ctx, r.ORM(), limit)
}
