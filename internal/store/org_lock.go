package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The org lock, and the lock order every org-sensitive write follows.
//
// A row's org changes in two ways only, both under its owner's org lock:
//
//   - the lazy backfill (AssignUnassignedOrg) stamps a NULL org_id;
//   - a project move (MoveProjectOrg, control-plane
//     docs/design/org-project-move.md) restamps one project with its chats,
//     their runs and its triggers, from the source org to the target, in one
//     transaction.
//
// Every other org_id is fixed when the row is written. So a write that
// decided on an org (who may act on these rows, which org a moved or new row
// takes) is only correct while neither runs for the same user between its
// decision and its commit.
//
// LockUserOrgs serializes them: the backfill takes it for each batch, a
// project move takes it before any row lock of its transaction, and every
// org-sensitive write takes it first in its transaction. Inside it, the
// write re-reads the orgs its decision read (OrgRead), or reads its rows and
// decides then, and writes; a decision a move overtook refuses with
// ErrOrgChanged rather than writing a source-org row into a moved project. A
// NULL org is no special case: compared IS NOT DISTINCT FROM, it is as fixed
// under the lock as any other.
//
// Lock order: the user's org lock, then row locks (FOR UPDATE / FOR NO KEY
// UPDATE / FOR SHARE), never the reverse. The backfill takes the org lock
// first too, and its row locks are SKIP LOCKED, so it never waits on a row
// a write holds. A project move takes it, then its project_org_moves row, the
// project, and the project's chats in id order, before it writes.
// TestOrgSensitiveWritesTakeTheOrgLock (internal/db) fails a
// write of the listed kinds that does not take it.

// OrgRead is the org a decision read from one row: compared under the lock,
// a row whose org is no longer Org (a backfill stamped it, or it is gone)
// refuses the write with ErrOrgChanged.
type OrgRead struct {
	// Table is one of the tables with an org: projects, chats, triggers,
	// workflow_drafts.
	Table string
	ID    string
	// Org is the org_id the decision read; nil for none.
	Org *string
}

// ErrOrgChanged refuses a write whose org decision no longer holds under the
// org lock.
var ErrOrgChanged = svcerr.FailedPrecondition("an organization changed while this was being saved; try again")

// orgReadSQL re-reads one row's org; the table names are fixed, never input.
var orgReadSQL = map[string]string{
	"projects":        `SELECT org_id FROM projects WHERE id = ?`,
	"chats":           `SELECT org_id FROM chats WHERE id = ?`,
	"triggers":        `SELECT org_id FROM triggers WHERE id = ?`,
	"workflow_drafts": `SELECT org_id FROM workflow_drafts WHERE id = ?`,
}

// userOrgLockSQL is the per-user advisory lock. Its key is the backfill's
// since #2537's first release, so a backfill of the previous release, during
// a rolling deploy, serializes with this one's writes too.
const userOrgLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('org-backfill:' || ?, 0))`

// LockUserOrgs takes userID's org lock for the rest of the surrounding
// transaction, then checks each read: its row still has the org the decision
// read. It must run inside a transaction (the lock is transaction-scoped)
// and before any row lock of that transaction.
func LockUserOrgs(ctx context.Context, db orm.Context, userID string, reads ...OrgRead) error {
	if userID == "" {
		return fmt.Errorf("org lock: user is required")
	}
	if _, err := db.Bun().NewRaw(userOrgLockSQL, userID).Exec(ctx); err != nil {
		return fmt.Errorf("take the org lock: %w", err)
	}
	for _, read := range reads {
		query, ok := orgReadSQL[read.Table]
		if !ok {
			return fmt.Errorf("org lock: %q has no org", read.Table)
		}
		var rows []struct {
			OrgID *string `bun:"org_id"`
		}
		if err := db.Bun().NewRaw(query, read.ID).Scan(ctx, &rows); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("re-read the org of %s %s: %w", read.Table, read.ID, err)
		}
		if len(rows) == 0 || !sameOrg(rows[0].OrgID, read.Org) {
			return ErrOrgChanged
		}
	}
	return nil
}

// sameOrg is IS NOT DISTINCT FROM.
func sameOrg(stored, read *string) bool {
	if stored == nil || read == nil {
		return stored == nil && read == nil
	}
	return *stored == *read
}
