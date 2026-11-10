package store

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/uptrace/bun"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// whereOrg narrows a list to scope's org: `org_id = ?`, or, when the scope
// includes the rows with no org yet, `(org_id = ? OR org_id IS NULL)`, grouped
// so it ANDs with the list's other predicates. idx_<table>_user_org serves
// both with the user_id predicate every list carries.
func whereOrg(column string, scope *core.OrgScope) orm.QueryOption {
	return func(q *bun.SelectQuery) {
		if !scope.IncludeUnassigned {
			q.Where("? = ?", bun.Ident(column), scope.OrgID)
			return
		}
		q.WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			return sq.Where("? = ?", bun.Ident(column), scope.OrgID).WhereOr("? IS NULL", bun.Ident(column))
		})
	}
}

// AssignUnassignedOrg gives userID's projects, chats, triggers, saved
// workflows and runs that have no org yet orgID, their default org: the lazy
// backfill of the rows written before orgs (internal/orgs ListScope). A chat
// or trigger whose project has an org takes the project's, so they agree with
// their project, and a run takes its chat's. It only ever fills NULL, so it is
// idempotent and never moves a row between orgs.
//
// Called with the ctx of a transaction it joins it; the caller runs both
// statements in one, so a chat is never stamped against a project that was not.
func AssignUnassignedOrg(ctx context.Context, db orm.Context, userID, orgID string) error {
	if userID == "" || orgID == "" {
		return fmt.Errorf("assign unassigned org: user and org are required")
	}
	if _, err := db.Bun().NewUpdate().Model((*Project)(nil)).
		Set("org_id = ?", orgID).
		Where("user_id = ?", userID).
		Where("org_id IS NULL").
		Exec(ctx); err != nil {
		return fmt.Errorf("assign unassigned org to projects: %w", err)
	}
	if _, err := db.Bun().NewRaw(`
		UPDATE chats c
		SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = c.project_id), ?)
		WHERE c.user_id = ? AND c.org_id IS NULL`, orgID, userID).Exec(ctx); err != nil {
		return fmt.Errorf("assign unassigned org to chats: %w", err)
	}
	if _, err := db.Bun().NewRaw(`
		UPDATE triggers t
		SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = t.project_id), ?)
		WHERE t.user_id = ? AND t.org_id IS NULL`, orgID, userID).Exec(ctx); err != nil {
		return fmt.Errorf("assign unassigned org to triggers: %w", err)
	}
	if _, err := db.Bun().NewRaw(`
		UPDATE workflow_drafts SET org_id = ? WHERE user_id = ? AND org_id IS NULL`, orgID, userID).Exec(ctx); err != nil {
		return fmt.Errorf("assign unassigned org to saved workflows: %w", err)
	}
	// A run's org is its chat's. Driven by idx_workflows_chat_id from the
	// user's chats.
	if _, err := db.Bun().NewRaw(`
		UPDATE workflows w
		SET org_id = c.org_id
		FROM chats c
		WHERE w.chat_id = c.id AND c.user_id = ? AND c.org_id IS NOT NULL AND w.org_id IS NULL`, userID).Exec(ctx); err != nil {
		return fmt.Errorf("assign unassigned org to runs: %w", err)
	}
	return nil
}
