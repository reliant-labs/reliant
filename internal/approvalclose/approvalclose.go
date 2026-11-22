// Copyright (c) 2025 Reliant Labs
//
// Package approvalclose closes the approvals nobody can answer and announces
// the close on the chat feed.
//
//forge:exclude-contract: a transaction around two store calls over consumer-declared interfaces; the consumers declare what they use
package approvalclose

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
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

// Repo is what a close writes through the repository: its transaction, the
// chat feed update and the chat's activity announcement.
type Repo interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	EmitChatActivityIfChanged(ctx context.Context, chatID string) error
}

// Closer closes pending approvals nobody can answer any more.
type Closer struct {
	repo      Repo
	approvals store.ApprovalExtStore
}

// New returns a Closer over repo and the approvals' ext store.
func New(repo Repo, approvals store.ApprovalExtStore) *Closer {
	return &Closer{repo: repo, approvals: approvals}
}

// NewFromRepo returns a Closer over the repository's own approvals.
func NewFromRepo(r *db.Repo) *Closer { return New(r, store.NewApprovalExtStore(r.ORM())) }

// ResolvePendingApproval closes a pending approval no one can act on any more
// with status (EXPIRED or CANCELLED), recording reason and actionTaken, and
// announces it on its chat's feed in the same transaction. It reports false,
// changing nothing, when the approval is no longer pending.
func (c *Closer) ResolvePendingApproval(ctx context.Context, id string, status int32, reason, actionTaken string) (bool, error) {
	var closed *core.Approval
	err := c.repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		closed = nil
		a, err := c.approvals.ResolvePendingApproval(ctx, id, reliantv1.ApprovalStatus(status), reason, actionTaken, time.Now().UTC())
		if err != nil || a == nil {
			return err
		}
		closed = a
		return c.AnnounceApprovalResolution(ctx, a)
	})
	if err != nil {
		return false, err
	}
	if closed == nil {
		return false, nil
	}
	// The chat may stop reading AWAITING_INPUT.
	if err := c.repo.EmitChatActivityIfChanged(ctx, closed.ChatID); err != nil {
		logging.WarnContext(ctx, "Failed to announce chat activity after closing an approval", "approvalID", id, "chatID", closed.ChatID, "error", err)
	}
	return true, nil
}

// AnnounceApprovalResolution writes the chat feed update that tells clients
// an approval is resolved, with its status, reason and when.
func (c *Closer) AnnounceApprovalResolution(ctx context.Context, a *core.Approval) error {
	status := db.ApprovalFeedStatus(a.Status)
	if a.Status == int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_DENIED) && a.ActionTaken != nil && *a.ActionTaken == "timeout" {
		// Timed out before EXPIRED existed: nobody denied it.
		status = db.ApprovalFeedStatus(int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_EXPIRED))
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
	db.AttributeApprovalUpdate(update, a)
	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("marshal approval resolution: %w", err)
	}
	return c.repo.CreateChatUpdate(ctx, a.ChatID, db.UpdateTypeApproval, a.ID, string(data))
}
