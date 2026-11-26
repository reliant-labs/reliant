package store

import (
	"context"
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
//
// Shared mode (LockUserOrgsShared). What the lock protects a write from is an
// org CHANGE, and only the backfill and the moves (a project's, a chat's)
// change one. A write that only needs the orgs it read to stay fixed until it
// commits, and that relies on the lock for nothing else, may take it shared:
// shared holders run beside one another and every exclusive holder still
// excludes them, both ways. A launch is such a write: everything else it must
// keep apart from another launch has its own guard (launch.launchNew), and
// before the org lock existed launches of one user never serialized. Taken
// exclusive, it made a user's launches queue behind one another for the whole
// of each launch transaction, which is where a burst of one user's trigger
// fires (one webhook fanning out to dozens of triggers) left a StartChat
// waiting hundreds of milliseconds. A transaction that took the lock shared
// must never take it exclusive later: two holders upgrading would deadlock.
// The shared and exclusive forms share one key, so a release that takes it
// exclusive still excludes this one's shared holders during a rolling deploy.

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

// ErrOrgMoveTargetHasProject refuses a project's move to an org where its
// owner already has a project at the same path, or of the same repository:
// projects are unique per (org, user, path) and (org, user, remote_url).
var ErrOrgMoveTargetHasProject = svcerr.FailedPrecondition("the target organization already has a project at this path or of this repository")

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

// userOrgLockSharedSQL is userOrgLockSQL's shared form, on the same key.
const userOrgLockSharedSQL = `SELECT pg_advisory_xact_lock_shared(hashtextextended('org-backfill:' || ?, 0))`

// LockUserOrgsShared is LockUserOrgs in shared mode (see "Shared mode"
// above): it waits for, and keeps out, every exclusive holder of userID's org
// lock — the backfill, a project or chat move, every other org-sensitive
// write — but not another shared holder. Same rules otherwise: inside a
// transaction, before any row lock of it, and never followed by LockUserOrgs
// in the same transaction.
func LockUserOrgsShared(ctx context.Context, db orm.Context, userID string, reads ...OrgRead) error {
	if userID == "" {
		return fmt.Errorf("org lock: user is required")
	}
	return lockUserOrgs(ctx, db, userOrgLockSharedSQL, userID, reads)
}

// sameOrg is IS NOT DISTINCT FROM.
func sameOrg(stored, read *string) bool {
	if stored == nil || read == nil {
		return stored == nil && read == nil
	}
	return *stored == *read
}
