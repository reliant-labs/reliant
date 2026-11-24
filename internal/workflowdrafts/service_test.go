package workflowdrafts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflowdrafts"
)

// inTx is a TxRunner that runs the write inline and records the policy it was
// asked to run at.
type inTx struct{ policy *orm.TxPolicy }

func (r inTx) RunTx(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error {
	*r.policy = policy
	return fn(ctx)
}

type rig struct {
	svc     workflowdrafts.Service
	drafts  *workflowdrafts.MockDrafts
	locks   *workflowdrafts.MockOrgLocks
	updates *workflowdrafts.MockUserUpdates
	policy  orm.TxPolicy
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{drafts: &workflowdrafts.MockDrafts{}, locks: &workflowdrafts.MockOrgLocks{}, updates: &workflowdrafts.MockUserUpdates{}}
	r.locks.LockUserOrgsFunc = func(context.Context, string, ...store.OrgRead) error { return nil }
	r.updates.CreateUserUpdateFunc = func(context.Context, *core.UserUpdate) error { return nil }
	svc, err := workflowdrafts.New(workflowdrafts.Deps{Tx: inTx{&r.policy}, Locks: r.locks, Updates: r.updates, Drafts: r.drafts})
	require.NoError(t, err)
	r.svc = svc
	return r
}

type member map[string]bool

func (m member) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return m[orgID], true, nil
}

// A draft write runs at READ COMMITTED with the retry budget the facade gave
// it: the user_updates counter row needs it (under SERIALIZABLE the write
// aborts against any concurrent update for the same user).
func TestWrites_RunReadCommittedWithTheFacadesRetryBudget(t *testing.T) {
	r := newRig(t)
	r.drafts.InsertWorkflowDraftFunc = func(context.Context, *core.WorkflowDraft) error { return nil }
	r.drafts.LoadWorkflowDraftFunc = func(_ context.Context, id string) (*core.WorkflowDraft, error) {
		return &core.WorkflowDraft{ID: id, UserID: "u", Slug: "s", Version: 1}, nil
	}
	require.NoError(t, r.svc.CreateWorkflowDraft(context.Background(), &core.WorkflowDraft{ID: "d1", UserID: "u", Slug: "s"}))
	require.Equal(t, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), r.policy)
}

// The owner's org lock is taken before the draft's row lock, and the write is
// announced in the same transaction.
func TestUpdateDefinition_LocksOrgThenRowThenWritesThenPublishes(t *testing.T) {
	r := newRig(t)
	var order []string
	r.drafts.WorkflowDraftOwnerFunc = func(context.Context, string) (string, error) { order = append(order, "owner"); return "u", nil }
	r.locks.LockUserOrgsFunc = func(context.Context, string, ...store.OrgRead) error { order = append(order, "org-lock"); return nil }
	r.drafts.LockWorkflowDraftFunc = func(_ context.Context, id string) (*core.WorkflowDraft, error) {
		order = append(order, "row-lock")
		return &core.WorkflowDraft{ID: id, UserID: "u", Version: 3}, nil
	}
	r.drafts.SetWorkflowDraftDefinitionFunc = func(context.Context, string, string, string, string, core.WorkflowDraftStatus) error {
		order = append(order, "write")
		return nil
	}
	r.drafts.LoadWorkflowDraftFunc = func(_ context.Context, id string) (*core.WorkflowDraft, error) {
		return &core.WorkflowDraft{ID: id, UserID: "u", Slug: "s", Version: 4}, nil
	}
	r.updates.CreateUserUpdateFunc = func(context.Context, *core.UserUpdate) error { order = append(order, "publish"); return nil }

	require.NoError(t, r.svc.UpdateWorkflowDraftDefinition(context.Background(), "d1", "n", "s", "name: n\n", core.WorkflowDraftStatusDraft))
	require.Equal(t, []string{"owner", "org-lock", "row-lock", "write", "publish"}, order)
}

// A write computed from a stale read is refused under the row lock and
// changes nothing: no definition write, no announcement.
func TestUpdateDefinitionAtVersion_StaleVersionWritesAndPublishesNothing(t *testing.T) {
	r := newRig(t)
	r.drafts.WorkflowDraftOwnerFunc = func(context.Context, string) (string, error) { return "u", nil }
	r.drafts.LockWorkflowDraftFunc = func(_ context.Context, id string) (*core.WorkflowDraft, error) {
		return &core.WorkflowDraft{ID: id, UserID: "u", Version: 5}, nil
	}
	err := r.svc.UpdateWorkflowDraftDefinitionAtVersion(context.Background(), "d1", 4, "n", "s", "x", core.WorkflowDraftStatusDraft)
	require.ErrorIs(t, err, workflowdrafts.ErrVersionConflict)
	require.Zero(t, r.drafts.CallCount("SetWorkflowDraftDefinition"))
	require.Zero(t, r.updates.CallCount("CreateUserUpdate"))
}

// A draft stamped into an org its owner has left is NotFound to a by-id
// write, as a missing one: it is not written and nothing is announced.
func TestByIDWrites_DecideOnTheLockedRowThroughAuthz(t *testing.T) {
	r := newRig(t)
	left := "globex"
	r.drafts.WorkflowDraftOwnerFunc = func(context.Context, string) (string, error) { return "u", nil }
	r.drafts.LockWorkflowDraftFunc = func(_ context.Context, id string) (*core.WorkflowDraft, error) {
		return &core.WorkflowDraft{ID: id, UserID: "u", OrgID: &left}, nil
	}
	ctx := authz.WithMembership(context.Background(), member{"home": true})

	_, err := r.svc.SetWorkflowDraftHidden(ctx, "d1", true)
	require.Error(t, err)
	_, err = r.svc.SetWorkflowDraftStatus(ctx, "d1", core.WorkflowDraftStatusComplete)
	require.Error(t, err)
	_, err = r.svc.UpdateWorkflowForkedFrom(ctx, "d1", "origin")
	require.Error(t, err)
	require.Error(t, r.svc.UpdateWorkflowDraft(ctx, &core.WorkflowDraft{ID: "d1"}))
	require.Zero(t, r.drafts.CallCount("SetWorkflowDraftHidden")+r.drafts.CallCount("SetWorkflowDraftStatus")+
		r.drafts.CallCount("SetWorkflowDraftForkedFrom")+r.drafts.CallCount("ReplaceWorkflowDraft"))
	require.Zero(t, r.updates.CallCount("CreateUserUpdate"))
}

// A unique violation on the slug is the domain error, with the slug in it.
func TestSlugConflict_IsTheDomainErrorNotTheDriversOne(t *testing.T) {
	r := newRig(t)
	r.drafts.InsertWorkflowDraftFunc = func(context.Context, *core.WorkflowDraft) error {
		return &pgconn.PgError{Code: "23505", ConstraintName: "workflow_drafts_user_id_slug_key"}
	}
	err := r.svc.CreateWorkflowDraft(context.Background(), &core.WorkflowDraft{ID: "d1", UserID: "u", Slug: "taken"})
	require.ErrorIs(t, err, workflowdrafts.ErrSlugTaken)
	require.Contains(t, err.Error(), `"taken"`)
	var pg *pgconn.PgError
	require.False(t, errors.As(err, &pg), "the driver error must not be wrapped, or the retry ladder would walk a permanent conflict")
}

// Reads answer nothing for a draft its owner may no longer read, and a slug
// the user has no draft of is nothing, not an error.
func TestReads_AreScopedThroughAuthz(t *testing.T) {
	r := newRig(t)
	left := "globex"
	hidden := &core.WorkflowDraft{ID: "d1", UserID: "u", Slug: "s", Name: "S", OrgID: &left, Status: core.WorkflowDraftStatusComplete}
	r.drafts.FindWorkflowDraftBySlugFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) { return hidden, nil }
	r.drafts.FindWorkflowDraftByNameFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) { return hidden, nil }
	r.drafts.FindUsableWorkflowBySlugFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) { return hidden, nil }
	r.drafts.ListUserWorkflowDraftsFunc = func(context.Context, string) ([]*core.WorkflowDraft, error) {
		return []*core.WorkflowDraft{hidden, {ID: "d2", UserID: "u", Slug: "own"}}, nil
	}
	ctx := authz.WithMembership(context.Background(), member{"home": true})

	got, err := r.svc.GetWorkflowDraftBySlug(ctx, "u", "s")
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = r.svc.GetWorkflowDraftByName(ctx, "u", "S")
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = r.svc.GetUsableWorkflowBySlug(ctx, "u", "s")
	require.NoError(t, err)
	require.Nil(t, got)
	listed, err := r.svc.ListWorkflowDraftsByUser(ctx, "u")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, "d2", listed[0].ID)
}

// A visible unfinished draft is reported not runnable, rather than hidden
// behind "not found"; a hidden one and a missing one are nothing.
func TestUsable_ReportsAVisibleDraftAsNotRunnable(t *testing.T) {
	r := newRig(t)
	r.drafts.FindUsableWorkflowBySlugFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) { return nil, nil }
	draft := &core.WorkflowDraft{ID: "d1", UserID: "u", Slug: "s", Status: core.WorkflowDraftStatusDraft}
	r.drafts.FindWorkflowDraftBySlugFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) { return draft, nil }
	ctx := authz.WithMembership(context.Background(), member{})

	got, err := r.svc.GetUsableWorkflowBySlug(ctx, "u", "s")
	require.Nil(t, got)
	var notRunnable *core.WorkflowDraftNotRunnableError
	require.ErrorAs(t, err, &notRunnable)

	draft.IsHidden = true
	got, err = r.svc.GetUsableWorkflowBySlug(ctx, "u", "s")
	require.NoError(t, err)
	require.Nil(t, got)
}

// An upsert onto a slug whose row its owner may not write is the slug taken,
// and nothing is overwritten.
func TestUpsert_RefusesASlugWhoseRowTheCallerMayNotWrite(t *testing.T) {
	r := newRig(t)
	left := "globex"
	r.drafts.LockWorkflowDraftBySlugFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) {
		return &core.WorkflowDraft{ID: "old", UserID: "u", Slug: "s", OrgID: &left}, nil
	}
	ctx := authz.WithMembership(context.Background(), member{"home": true})
	_, err := r.svc.UpsertWorkflowDraft(ctx, &core.WorkflowDraft{ID: "new", UserID: "u", Slug: "s"})
	require.ErrorIs(t, err, workflowdrafts.ErrSlugTaken)
	require.Zero(t, r.drafts.CallCount("UpsertWorkflowDraftBySlug"))
}

// A delete under the org lock of a draft its owner may not write deletes
// nothing and announces nothing.
func TestDelete_LeavesADraftTheCallerMayNotWrite(t *testing.T) {
	r := newRig(t)
	left := "globex"
	r.drafts.LockWorkflowDraftBySlugFunc = func(context.Context, string, string) (*core.WorkflowDraft, error) {
		return &core.WorkflowDraft{ID: "d1", UserID: "u", Slug: "s", OrgID: &left}, nil
	}
	ctx := authz.WithMembership(context.Background(), member{"home": true})
	require.NoError(t, r.svc.DeleteWorkflowDraftBySlug(ctx, "u", "s"))
	require.Zero(t, r.drafts.CallCount("RemoveWorkflowDraft"))
	require.Zero(t, r.updates.CallCount("CreateUserUpdate"))
}
