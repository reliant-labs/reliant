package db

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
)

// A saved-workflow write decides on its row under its owner's org lock, so
// what changed between the caller's own check and the write is what it
// decides on: a backfill that stamped the draft into an org the caller has
// left, or an insert of such a draft into a slug that was free. Neither is
// overwritten nor deleted.
func TestWorkflowDrafts_WritesDecideOnTheRowUnderTheOrgLock(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	userID := "user-draft-lock-" + uuid.NewString()[:8]
	ctx := authz.WithMembership(context.Background(), liveIn{"home": true})
	stored := func(id string) *WorkflowDraft {
		t.Helper()
		d, err := repo.GetWorkflowDraft(context.Background(), id)
		require.NoError(t, err)
		return d
	}

	// Two drafts with no org: the caller may change them, so a check made
	// now admits an upsert and a delete.
	upserted, deleted := newDraftForTest(userID), newDraftForTest(userID)
	for _, d := range []*WorkflowDraft{upserted, deleted} {
		require.NoError(t, repo.CreateWorkflowDraft(ctx, d))
	}
	// The backfill commits between that check and the writes, stamping both
	// with acme, which the caller has left.
	require.NoError(t, repo.AssignUnassignedOrg(context.Background(), userID, "acme"))

	overwrite := *upserted
	overwrite.ID = uuid.NewString()
	overwrite.Definition = "name: overwritten\n"
	_, err := repo.UpsertWorkflowDraft(ctx, &overwrite)
	require.ErrorIs(t, err, ErrWorkflowSlugTaken, "an upsert over a draft stamped into a left org")
	require.Equal(t, upserted.Definition, stored(upserted.ID).Definition)

	require.NoError(t, repo.DeleteWorkflowDraftBySlug(ctx, userID, deleted.Slug))
	require.NotNil(t, stored(deleted.ID), "a delete of a draft stamped into a left org deletes nothing")

	// A slug free when the caller checked it: another write inserts acme's
	// draft there first. The upsert does not overwrite it.
	intervening := newDraftForTest(userID)
	acme := "acme"
	intervening.OrgID = &acme
	require.NoError(t, repo.CreateWorkflowDraft(context.Background(), intervening))
	fresh := newDraftForTest(userID)
	fresh.Slug, fresh.Definition = intervening.Slug, "name: mine\n"
	_, err = repo.UpsertWorkflowDraft(ctx, fresh)
	require.ErrorIs(t, err, ErrWorkflowSlugTaken, "an upsert into a slug a denied draft took")
	require.Equal(t, intervening.Definition, stored(intervening.ID).Definition)

	// Each by-id write decides on the locked row too.
	_, err = repo.SetWorkflowDraftHidden(ctx, upserted.ID, true)
	require.Error(t, err, "hide a draft stamped into a left org")
	require.False(t, stored(upserted.ID).IsHidden)
}

// The backfill waits behind a write that holds its user's org lock, and runs
// once the write commits: it never stamps between a write's decision and
// its commit.
func TestAssignUnassignedOrg_WaitsBehindAWriteHoldingTheOrgLock(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	userID := "user-org-lock-" + uuid.NewString()[:8]
	ctx := context.Background()
	draft := newDraftForTest(userID)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))

	locked, release := make(chan struct{}), make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			if err := repo.LockUserOrgs(ctx, userID, OrgRead{Table: "workflow_drafts", ID: draft.ID}); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case err := <-writeDone:
		t.Fatalf("the write ended before it held the lock: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the write never took the org lock")
	}

	var backfilled atomic.Bool
	backfillDone := make(chan error, 1)
	go func() {
		err := repo.AssignUnassignedOrg(ctx, userID, "acme")
		backfilled.Store(true)
		backfillDone <- err
	}()
	// The backfill is queued on the advisory lock, not stamping.
	require.Eventually(t, func() bool {
		var waiting int
		err := repo.ORM().Bun().NewRaw(`SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE l.locktype = 'advisory' AND NOT l.granted AND a.datname = current_database()`).Scan(ctx, &waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, 5*time.Millisecond, "the backfill never queued on the org lock")
	require.False(t, backfilled.Load(), "the backfill ran while a write held the org lock")
	got, err := repo.GetWorkflowDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.Nil(t, got.OrgID, "stamped under a write's org lock")

	close(release)
	require.NoError(t, <-writeDone)
	require.NoError(t, <-backfillDone)
	got, err = repo.GetWorkflowDraft(ctx, draft.ID)
	require.NoError(t, err)
	require.NotNil(t, got.OrgID)
	require.Equal(t, "acme", *got.OrgID, "stamped once the write committed")
}

// LockUserOrgs refuses a decision whose read no longer holds, NULL included.
func TestLockUserOrgs_ComparesEveryReadIsNotDistinctFrom(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	userID := "user-org-read-" + uuid.NewString()[:8]
	ctx := context.Background()
	draft := newDraftForTest(userID)
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
	acme := "acme"
	check := func(read OrgRead) error {
		return repo.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			return repo.LockUserOrgs(ctx, userID, read)
		})
	}

	require.NoError(t, check(OrgRead{Table: "workflow_drafts", ID: draft.ID, Org: nil}), "NULL read, NULL row")
	require.True(t, errors.Is(check(OrgRead{Table: "workflow_drafts", ID: draft.ID, Org: &acme}), ErrOrgChanged), "acme read, NULL row")
	require.NoError(t, repo.AssignUnassignedOrg(ctx, userID, acme))
	require.True(t, errors.Is(check(OrgRead{Table: "workflow_drafts", ID: draft.ID, Org: nil}), ErrOrgChanged), "NULL read, a stamped row")
	require.NoError(t, check(OrgRead{Table: "workflow_drafts", ID: draft.ID, Org: &acme}))
	require.True(t, errors.Is(check(OrgRead{Table: "workflow_drafts", ID: uuid.NewString()}), ErrOrgChanged), "a row that is gone")
}
