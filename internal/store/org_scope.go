package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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

// orgBackfillStep reads the same candidates for the unlocked probe and the
// locked batch. It selects the ids it can stamp (?0 user, ?1 org), ignoring
// permanently blocked rows, such as a project colliding with the default org.
// The batch adds its keyset cursor (?2, "" to start) and size (?3). Its SKIP
// LOCKED leaves rows held by concurrent writers to a later pass.
type orgBackfillStep struct {
	table      string
	candidates string
	key        string // selected id, qualified when needed for a join
	lock       string // FOR UPDATE target; required for outer joins
	update     string
}

func (s orgBackfillStep) batchSQL() string {
	return `WITH batch AS (` + s.candidates + `
    AND ` + s.key + ` > ?2
    ORDER BY ` + s.key + ` LIMIT ?3
    FOR UPDATE` + s.lock + ` SKIP LOCKED
), stamped AS (` + s.update + `)
SELECT id FROM stamped ORDER BY id`
}

// orgBackfillSteps run in order: every project of the user first, then the
// chats and triggers, each of which takes its project's committed org (never
// a default of its own), so they agree with their project however passes
// interleave. Saved workflows belong to no project and take the default. Runs
// come last and take their chat's committed org.
var orgBackfillSteps = []orgBackfillStep{
	// A project is unique per (org, user, path) and (org, user, remote_url)
	// (projects_org_user_path_key, projects_org_user_remote_url_key), and an
	// unassigned row is keyed apart from every org's. One whose path or
	// remote the default org already holds is left unassigned rather than
	// stamped into a collision: it still lists with the default org
	// (OrgScope.IncludeUnassigned), and a violation would fail the whole
	// batch. The create-time checks refuse making such a pair, so only a
	// create that could not resolve an org can leave one.
	{"projects", `
    SELECT p.id FROM projects p
    WHERE p.user_id = ?0 AND p.org_id IS NULL
      AND NOT EXISTS (
          SELECT 1 FROM projects o
          WHERE o.user_id = p.user_id AND o.org_id = ?1
            AND (o.path = p.path OR o.remote_url = p.remote_url)
      )`, "p.id", " OF p", `
    UPDATE projects p SET org_id = ?1
    FROM batch WHERE p.id = batch.id
    RETURNING p.id
`},
	// A chat in a project only ever takes its project's committed org: one
	// whose project has no org yet is left for a later pass (it is stamped
	// once the project is), never given a default its project may not get.
	// A chat with no project takes the default.
	{"chats", `
    SELECT c.id FROM chats c
    LEFT JOIN projects p ON p.id = c.project_id
    WHERE c.user_id = ?0 AND c.org_id IS NULL
      AND (p.id IS NULL OR p.org_id IS NOT NULL)`, "c.id", " OF c", `
    UPDATE chats c SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = c.project_id), ?1)
    FROM batch WHERE c.id = batch.id
    RETURNING c.id
`},
	// A trigger in a project takes its project's committed org, as a chat
	// does, and waits for a project still unassigned.
	{"triggers", `
    SELECT t.id FROM triggers t
    LEFT JOIN projects p ON p.id = t.project_id
    WHERE t.user_id = ?0 AND t.org_id IS NULL
      AND (p.id IS NULL OR p.org_id IS NOT NULL)`, "t.id", " OF t", `
    UPDATE triggers t SET org_id = COALESCE((SELECT p.org_id FROM projects p WHERE p.id = t.project_id), ?1)
    FROM batch WHERE t.id = batch.id
    RETURNING t.id
`},
	{"saved workflows", `
    SELECT id FROM workflow_drafts
    WHERE user_id = ?0 AND org_id IS NULL`, "id", "", `
    UPDATE workflow_drafts d SET org_id = ?1
    FROM batch WHERE d.id = batch.id
    RETURNING d.id
`},
	// A run's org is its chat's committed org; runs carry no user, so the
	// batch is drawn through the user's chats. A user may have ~100k chats,
	// so both read from the few runs with no org
	// (idx_workflows_unassigned_org, a partial index), not from the chats.
	// ?1 is unused.
	{"runs", `
    SELECT w.id FROM workflows w
    JOIN chats c ON c.id = w.chat_id
    WHERE c.user_id = ?0 AND c.org_id IS NOT NULL AND w.org_id IS NULL`, "w.id", " OF w", `
    UPDATE workflows w SET org_id = c.org_id
    FROM batch, chats c WHERE w.id = batch.id AND c.id = w.chat_id
    RETURNING w.id
`},
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
//
// When no candidates remain, one indexed EXISTS skips the entire pass
// without taking the org lock. On a positive probe every step still selects
// its candidates under the lock: another pass may have stamped them, new
// rows may have appeared, or stamping a parent may make a child eligible.
// Rows arriving after a negative probe list with the default org and can be
// stamped by the next pass, just like rows arriving after their batch.
func AssignUnassignedOrg(ctx context.Context, db orm.Context, userID, orgID string) error {
	return assignUnassignedOrg(ctx, db, userID, orgID, OrgBackfillBatch)
}

var orgBackfillPendingSQL = func() string {
	selects := make([]string, len(orgBackfillSteps))
	for i, step := range orgBackfillSteps {
		selects[i] = step.candidates + " AND " + step.key + " > ''"
	}
	return "SELECT EXISTS (" + strings.Join(selects, "\nUNION ALL") + "\n)"
}()

func assignUnassignedOrg(ctx context.Context, db orm.Context, userID, orgID string, batch int) error {
	if userID == "" || orgID == "" {
		return fmt.Errorf("assign unassigned org: user and org are required")
	}
	var pending bool
	if err := db.Bun().NewRaw(orgBackfillPendingSQL, userID, orgID).Scan(ctx, &pending); err != nil {
		return fmt.Errorf("assign unassigned org: find unassigned rows: %w", err)
	}
	if !pending {
		return nil
	}
	for _, step := range orgBackfillSteps {
		batchSQL := step.batchSQL()
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
				if err := db.Bun().NewRaw(batchSQL, userID, orgID, after, batch).Scan(ctx, &ids); err != nil && !errors.Is(err, sql.ErrNoRows) {
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
