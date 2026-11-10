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

// orgBackfillSteps run in order: every project of the user first, so each
// chat batch reads its project's final org. A chat whose project has an org
// takes the project's, so a chat and its project agree; one whose project has
// none takes the default, which is what that project is stamped with too.
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
	{"chats", `
WITH batch AS (
    SELECT id FROM chats
    WHERE user_id = ?0 AND org_id IS NULL AND id > ?2
    ORDER BY id LIMIT ?3
    FOR UPDATE SKIP LOCKED
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
// transaction under a short statement_timeout, resuming from the last id
// stamped, so a user with many rows never holds one long transaction over all
// of them. A failed batch leaves the earlier ones stamped; the next call
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
