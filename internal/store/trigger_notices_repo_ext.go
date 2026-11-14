// Copyright (c) 2025 Reliant Labs
package store

import (
	"context"
	"time"

	"github.com/uptrace/bun"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The notices of control-plane docs/design/org-provider-accounts.md §11.3 and
// §11.4: an automation paused because none of its org's accounts could run it,
// and the automations still on their owner's own accounts in a team org.

// noOrgAccountPause selects the triggers this release paused for want of an
// org account: still paused for it, with the time of the pause.
func noOrgAccountPause(q *bun.SelectQuery) *bun.SelectQuery {
	return q.Where("enabled = false").Where("paused_reason = ?", core.TriggerPausedNoOrgAccount).
		Where("paused_at IS NOT NULL").Where("org_id IS NOT NULL")
}

// HasNoOrgAccountPauses reports whether any trigger is paused for want of an
// org account, so an inbox read with none asks no one about anything.
func HasNoOrgAccountPauses(ctx context.Context, db orm.Context) (bool, error) {
	return noOrgAccountPause(db.Bun().NewSelect().Model((*Trigger)(nil))).Limit(1).Exists(ctx)
}

// ListNoOrgAccountPauses returns the triggers paused for want of an org account
// that userID owns, or that belong to one of adminOrgIDs (the orgs they own or
// administer), newest pause first.
func ListNoOrgAccountPauses(ctx context.Context, db orm.Context, userID string, adminOrgIDs []string) ([]*core.Trigger, error) {
	var rows []*Trigger
	q := noOrgAccountPause(db.Bun().NewSelect().Model(&rows))
	if len(adminOrgIDs) > 0 {
		q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("user_id = ?", userID).WhereOr("org_id IN (?)", bun.List(adminOrgIDs))
		})
	} else {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.Order("paused_at DESC", "id").Limit(200).Scan(ctx); err != nil {
		return nil, err
	}
	return triggersToCore(rows)
}

// ListUndeliveredPauseNotices returns up to limit triggers paused for want of an
// org account whose email to the org's admins was never confirmed delivered
// (pause_notice_for is not their paused_at), oldest pause first.
func ListUndeliveredPauseNotices(ctx context.Context, db orm.Context, limit int) ([]*core.Trigger, error) {
	var rows []*Trigger
	if err := noOrgAccountPause(db.Bun().NewSelect().Model(&rows)).
		Where("pause_notice_for IS DISTINCT FROM paused_at").
		Order("paused_at", "id").Limit(int64(limit)).Scan(ctx); err != nil {
		return nil, err
	}
	return triggersToCore(rows)
}

// ConfirmPauseNotice records that the email about trigger id's pause at
// pausedAt was delivered: the control plane sent it, or had already sent it
// (deduplicated), or there is no one to send it to. It writes nothing when the trigger has
// been resumed or paused again since: that pause's notice is its own.
func ConfirmPauseNotice(ctx context.Context, db orm.Context, id string, pausedAt time.Time) error {
	_, err := db.Bun().NewRaw(`UPDATE triggers SET pause_notice_for = paused_at
		WHERE id = ? AND enabled = false AND paused_reason = ? AND paused_at = ?`,
		id, core.TriggerPausedNoOrgAccount, pausedAt.UTC()).Exec(ctx)
	return err
}

// ListPersonallyFundedTriggers returns userID's triggers of an org whose runs
// may spend their own accounts: stamped any, or before the stamp (NULL),
// oldest first.
func ListPersonallyFundedTriggers(ctx context.Context, db orm.Context, userID string) ([]*core.Trigger, error) {
	var rows []*Trigger
	if err := db.Bun().NewSelect().Model(&rows).Where("user_id = ?", userID).Where("org_id IS NOT NULL").
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("unattended_funding IS NULL").WhereOr("unattended_funding = ?", core.UnattendedFundingAny)
		}).Order("created_at", "id").Scan(ctx); err != nil {
		return nil, err
	}
	return triggersToCore(rows)
}
