package store

import (
	"context"
	"database/sql"
	"errors"
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

// OrgBackfillBatch is how many rows one backfill transaction stamps.
const OrgBackfillBatch = 500

// orgBackfillStatementTimeout bounds each batch's statements: a batch that
// cannot finish quickly is abandoned (the next list retries it), never left
// holding row locks against the caller's own writes.
const orgBackfillStatementTimeout = "5s"

// orgBackfillStep stamps one batch of one table's rows: ?0 user, ?1 org,
// ?2 the last id stamped (the keyset cursor, "" to start), ?3 the batch size.
// It returns the ids it stamped, ordered as the cursor compares them (the
// database's collation, which a Go string comparison need not match). SKIP LOCKED leaves a row a
// concurrent write holds to a later pass rather than waiting on it.
type orgBackfillStep struct {
	table string
	sql   string
}

// orgBackfillSteps run in order: every project of the user first, then the
// chats and triggers, each of which takes its project's committed org (never
// a default of its own), so they agree with their project however passes
// interleave. Saved workflows belong to no project and take the default. Runs
// come last and take their chat's committed org.
var orgBackfillSteps = []orgBackfillStep{
	{"projects", `
WITH batch AS (
    SELECT id FROM projects
    WHERE user_id = ?0 AND org_id IS NULL AND id > ?2
    ORDER BY id LIMIT ?3
    FOR UPDATE SKIP LOCKED
), stamped AS (
    UPDATE projects p SET org_id = ?1
    FROM batch WHERE p.id = batch.id
    RETURNING p.id
)
SELECT id FROM stamped ORDER BY id`},
	// A chat in a project only ever takes its project's committed org: one
	// whose project has no org yet is left for a later pass (it is stamped
	// once the project is), never given a default its project may not get.
	// A chat with no project takes the default.
	{"chats", `
WITH batch AS (
    SELECT c.id FROM chats c
    LEFT JOIN projects p ON p.id = c.project_id
    WHERE c.user_id = ?0 AND c.org_id IS NULL AND c.id > ?2
      AND (p.id IS NULL OR p.org_id IS NOT NULL)
    ORDER BY c.id LIMIT ?3
    FOR UPDATE OF c SKIP LOCKED
), stamped AS (
    UPDATE chats c SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = c.project_id), ?1)
    FROM batch WHERE c.id = batch.id
    RETURNING c.id
)
SELECT id FROM stamped ORDER BY id`},
	// A trigger in a project takes its project's committed org, as a chat
	// does, and waits for a project still unassigned.
	{"triggers", `
WITH batch AS (
    SELECT t.id FROM triggers t
    LEFT JOIN projects p ON p.id = t.project_id
    WHERE t.user_id = ?0 AND t.org_id IS NULL AND t.id > ?2
      AND (p.id IS NULL OR p.org_id IS NOT NULL)
    ORDER BY t.id LIMIT ?3
    FOR UPDATE OF t SKIP LOCKED
), stamped AS (
    UPDATE triggers t SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = t.project_id), ?1)
    FROM batch WHERE t.id = batch.id
    RETURNING t.id
)
SELECT id FROM stamped ORDER BY id`},
	{"saved workflows", `
WITH batch AS (
    SELECT id FROM workflow_drafts
    WHERE user_id = ?0 AND org_id IS NULL AND id > ?2
    ORDER BY id LIMIT ?3
    FOR UPDATE SKIP LOCKED
), stamped AS (
    UPDATE workflow_drafts d SET org_id = ?1
    FROM batch WHERE d.id = batch.id
    RETURNING d.id
)
SELECT id FROM stamped ORDER BY id`},
	// A run's org is its chat's committed org; runs carry no user, so the
	// batch is drawn through the user's chats (idx_workflows_chat_id). ?1 is
	// unused.
	{"runs", `
WITH batch AS (
    SELECT w.id FROM workflows w
    JOIN chats c ON c.id = w.chat_id
    WHERE c.user_id = ?0 AND c.org_id IS NOT NULL AND w.org_id IS NULL AND w.id > ?2
    ORDER BY w.id LIMIT ?3
    FOR UPDATE OF w SKIP LOCKED
), stamped AS (
    UPDATE workflows w SET org_id = c.org_id
    FROM batch, chats c WHERE w.id = batch.id AND c.id = w.chat_id
    RETURNING w.id
)
SELECT id FROM stamped ORDER BY id`},
}

// AssignUnassignedOrg gives userID's projects, chats, triggers, saved
// workflows and runs that have no org yet orgID, their default org: the lazy
// backfill of the rows written before orgs (internal/orgs ListScope). A chat
// or trigger in a project takes the project's org, so they agree with their
// project, and a run takes its chat's. It only ever fills NULL, so it is
// idempotent and never moves a row between orgs.
//
// It stamps in bounded batches (OrgBackfillBatch rows), each its own
// transaction under a short statement_timeout and a per-user advisory lock,
// resuming from the last id stamped, so a user with many rows never holds one
// long transaction over all of them. A row a concurrent write holds is
// skipped (SKIP LOCKED) and stamped by a later pass; a chat whose project was
// skipped waits for it. A failed batch leaves the earlier ones stamped; the next call
// resumes from what is still NULL. It must not be called inside a
// transaction, which would join every batch into it.
func AssignUnassignedOrg(ctx context.Context, db orm.Context, userID, orgID string) error {
	return assignUnassignedOrg(ctx, db, userID, orgID, OrgBackfillBatch)
}

func assignUnassignedOrg(ctx context.Context, db orm.Context, userID, orgID string, batch int) error {
	if userID == "" || orgID == "" {
		return fmt.Errorf("assign unassigned org: user and org are required")
	}
	for _, step := range orgBackfillSteps {
		after := ""
		for {
			var ids []string
			err := db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
				if _, err := db.Bun().NewRaw("SET LOCAL statement_timeout = '" + orgBackfillStatementTimeout + "'").Exec(ctx); err != nil {
					return err
				}
				// One user's batches run one at a time, whichever pass
				// they belong to, so two passes with different defaults
				// never stamp the same user's rows concurrently; and never
				// between an org-sensitive write's decision and its commit
				// (LockUserOrgs, org_lock.go).
				if err := LockUserOrgs(ctx, db, userID); err != nil {
					return err
				}
				if err := db.Bun().NewRaw(step.sql, userID, orgID, after, batch).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("assign unassigned org to %s: %w", step.table, err)
			}
			if len(ids) < batch {
				break
			}
			after = ids[len(ids)-1]
		}
	}
	return nil
}
