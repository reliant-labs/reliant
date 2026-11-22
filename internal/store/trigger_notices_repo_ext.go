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

// ClaimDuePauseNotices claims up to limit triggers paused for want of an org
// account whose email to the org's admins is still owed and due: never
// confirmed delivered for that pause (pause_notice_for), never refused for it
// (pause_notice_refused_for), and pause_notice_next_at passed or unset. Each is
// claimed by moving pause_notice_next_at to now + lease, so the next sweep
// takes the ones after it: a pause whose workflow is still running, waiting
// for an upgraded control plane, or failing to start comes round again after
// lease and never holds the batch from a newer one. Oldest due first; SKIP
// LOCKED, so two workers' sweeps claim different rows.
func ClaimDuePauseNotices(ctx context.Context, db orm.Context, limit int, lease time.Duration) ([]*core.Trigger, error) {
	var ids []string
	if err := db.Bun().NewRaw(`UPDATE triggers SET pause_notice_next_at = now() + make_interval(secs => ?)
		WHERE id IN (
			SELECT id FROM triggers
			 WHERE enabled = false AND paused_reason = ? AND paused_at IS NOT NULL AND org_id IS NOT NULL
			   AND pause_notice_for IS DISTINCT FROM paused_at
			   AND pause_notice_refused_for IS DISTINCT FROM paused_at
			   AND (pause_notice_next_at IS NULL OR pause_notice_next_at <= now())
			 ORDER BY pause_notice_next_at NULLS FIRST, paused_at, id
			 LIMIT ? FOR UPDATE SKIP LOCKED)
		RETURNING id`, lease.Seconds(), core.TriggerPausedNoOrgAccount, limit).Scan(ctx, &ids); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []*Trigger
	if err := db.Bun().NewSelect().Model(&rows).Where("id IN (?)", bun.List(ids)).Order("paused_at", "id").Scan(ctx); err != nil {
		return nil, err
	}
	return triggersToCore(rows)
}

// RefusePauseNotice records that the control plane refused for good to email
// about trigger id's pause at pausedAt (the org is gone): the sweep never
// starts that pause's notice again. Like ConfirmPauseNotice it writes nothing
// for a pause since resumed or re-paused.
func RefusePauseNotice(ctx context.Context, db orm.Context, id string, pausedAt time.Time) error {
	_, err := db.Bun().NewRaw(`UPDATE triggers SET pause_notice_refused_for = paused_at
		WHERE id = ? AND enabled = false AND paused_reason = ? AND paused_at = ?`,
		id, core.TriggerPausedNoOrgAccount, pausedAt.UTC()).Exec(ctx)
	return err
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
