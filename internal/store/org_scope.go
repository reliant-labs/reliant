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
// chats, each of which takes its project's committed org (never a default of
// its own), so a chat and its project agree however passes interleave.
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
}

// AssignUnassignedOrg gives userID's projects and chats that have no org yet
// orgID, their default org: the lazy backfill of the rows written before orgs
// (internal/orgs ListScope). It only ever fills NULL, so it is idempotent and
// never moves a row between orgs.
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
				// never stamp the same user's rows concurrently.
				if _, err := db.Bun().NewRaw("SELECT pg_advisory_xact_lock(hashtextextended('org-backfill:' || ?, 0))", userID).Exec(ctx); err != nil {
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
