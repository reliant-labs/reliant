// Copyright (c) 2025 Reliant Labs
package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The integration access and write-ledger ports (P5b lane L5). Each ext query
// is written through one layer and read back through the other (env.Repo is
// the Repository, which delegates to these functions once ported), and pins
// the rows the sqlc twin returned.

func accessGrants(t *testing.T, env *storetest.Env, userID, integrationID string) map[string]store.IntegrationEventAccess {
	t.Helper()
	rows, err := store.ListIntegrationEventAccess(context.Background(), env.ORM)
	require.NoError(t, err)
	out := map[string]store.IntegrationEventAccess{}
	for _, r := range rows {
		if r.UserID == userID && r.IntegrationID == integrationID {
			out[r.AccountKey+"/"+r.ResourceKey] = *r
		}
	}
	return out
}

func TestReplaceIntegrationAccessIsExactAndMonotonic(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond)

	require.NoError(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "alice", "github", "11", t0, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1", ResourceLabel: "acme/app"},
		{AccountKey: "100", ResourceKey: "2", ResourceLabel: "acme/secret"},
	}))
	got := accessGrants(t, env, "alice", "github")
	require.Len(t, got, 2)
	assert.Equal(t, "acme/secret", got["100/2"].ResourceLabel)
	assert.Equal(t, "11", got["100/2"].SubjectID)
	assert.True(t, got["100/1"].RefreshedAt.Equal(t0))

	// The Repository writes the same set: repo 2 is lost, repo 3 appears.
	t1 := t0.Add(time.Minute)
	require.NoError(t, env.Repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", t1, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1", ResourceLabel: "acme/app-renamed"},
		{AccountKey: "100", ResourceKey: "3", ResourceLabel: "acme/new"},
	}))
	got = accessGrants(t, env, "alice", "github")
	require.Len(t, got, 2, "the refresh replaces the set exactly")
	assert.Equal(t, "acme/app-renamed", got["100/1"].ResourceLabel)
	assert.Contains(t, got, "100/3")
	assert.NotContains(t, got, "100/2")

	// A slower refresh that started earlier never stamps an older time over
	// a newer one's, and drops nothing the newer one recorded.
	require.NoError(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "alice", "github", "11", t0, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1", ResourceLabel: "stale"},
	}))
	got = accessGrants(t, env, "alice", "github")
	require.Len(t, got, 2)
	assert.Equal(t, "acme/app-renamed", got["100/1"].ResourceLabel)
	assert.True(t, got["100/1"].RefreshedAt.Equal(t1))

	// An empty refresh (the credential is gone) drops the whole set.
	require.NoError(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "alice", "github", "11", t1.Add(time.Minute), nil))
	assert.Empty(t, accessGrants(t, env, "alice", "github"))

	assert.Error(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "", "github", "", t1, nil))
	assert.Error(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "alice", "github", "", t1,
		[]core.IntegrationAccessGrant{{AccountKey: "100"}}), "a grant needs a resource")
}

func TestRevokeAndPruneIntegrationAccess(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	seed := func() {
		require.NoError(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "alice", "github", "11", now, []core.IntegrationAccessGrant{
			{AccountKey: "100", ResourceKey: "1"}, {AccountKey: "100", ResourceKey: "2"}, {AccountKey: "200", ResourceKey: "9"},
		}))
		require.NoError(t, store.ReplaceIntegrationAccess(ctx, env.ORM, "bob", "github", "22", now, []core.IntegrationAccessGrant{
			{AccountKey: "100", ResourceKey: "1"},
		}))
	}
	seed()

	_, err := store.RevokeIntegrationAccess(ctx, env.ORM, "github", core.IntegrationAccessRevocation{ResourceKey: "1"})
	assert.Error(t, err, "a revocation must name an account or a subject")

	// A repository removed from an installation: every member loses it.
	n, err := store.RevokeIntegrationAccess(ctx, env.ORM, "github", core.IntegrationAccessRevocation{AccountKey: "100", ResourceKey: "1"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, n)
	// A member removed: only their own grants, narrowed to the account.
	n, err = env.Repo.RevokeIntegrationAccess(ctx, "github", core.IntegrationAccessRevocation{AccountKey: "100", SubjectID: "11"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	assert.Equal(t, []string{"200/9"}, keys(accessGrants(t, env, "alice", "github")))
	// An installation removed: everything through it.
	seed()
	n, err = store.RevokeIntegrationAccess(ctx, env.ORM, "github", core.IntegrationAccessRevocation{AccountKey: "100"})
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)

	// Prune drops what nobody re-confirmed since before.
	n, err = store.PruneIntegrationAccess(ctx, env.ORM, now)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "refreshed_at = before is kept")
	n, err = env.Repo.PruneIntegrationAccess(ctx, now.Add(time.Second))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
}

func keys(m map[string]store.IntegrationEventAccess) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestIntegrationAccessRefreshLease(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	_, err := store.GetIntegrationAccessRefresh(ctx, env.ORM, "alice", "github")
	assert.True(t, errors.Is(err, sql.ErrNoRows), "a pair never refreshed is sql.ErrNoRows, got %v", err)

	claimed, err := store.ClaimIntegrationAccessRefresh(ctx, env.ORM, "alice", "github", now, now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.True(t, claimed, "a pair never refreshed is claimed by the insert")
	claimed, err = env.Repo.ClaimIntegrationAccessRefresh(ctx, "alice", "github", now, now.Add(time.Minute), now.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, claimed, "another replica holds the lease")

	require.NoError(t, store.FinishIntegrationAccessRefresh(ctx, env.ORM, "alice", "github", now, false, "boom"))
	got, err := env.Repo.GetIntegrationAccessRefresh(ctx, "alice", "github")
	require.NoError(t, err)
	assert.Nil(t, got.RefreshedAt, "a failure does not move refreshed_at")
	assert.Equal(t, "boom", got.LastError)
	require.NotNil(t, got.LastAttemptAt)
	assert.True(t, got.LastAttemptAt.Equal(now))

	claimed, err = store.ClaimIntegrationAccessRefresh(ctx, env.ORM, "alice", "github", now, now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.False(t, claimed, "released, but the last attempt is not before dueBefore")
	later := now.Add(time.Hour)
	claimed, err = store.ClaimIntegrationAccessRefresh(ctx, env.ORM, "alice", "github", later, later.Add(time.Minute), later)
	require.NoError(t, err)
	assert.True(t, claimed)

	require.NoError(t, env.Repo.FinishIntegrationAccessRefresh(ctx, "alice", "github", later, nil))
	state, err := store.GetIntegrationAccessRefresh(ctx, env.ORM, "alice", "github")
	require.NoError(t, err)
	require.NotNil(t, state.RefreshedAt)
	assert.True(t, state.RefreshedAt.Equal(later))
	assert.Empty(t, state.LastError, "success clears the error")
	row, err := store.GetIntegrationAccessRefreshByPK(ctx, env.ORM, "alice", "github")
	require.NoError(t, err)
	assert.Nil(t, row.LeasedUntil, "finish releases the lease")
}

func TestListIntegrationTriggerOwners(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(userID, integration string, enabled bool) {
		cfg, err := json.Marshal(core.IntegrationConfig{Integration: integration, Events: []string{"issues.opened"}})
		require.NoError(t, err)
		require.NoError(t, env.Repo.CreateTrigger(ctx, &core.Trigger{
			ID: uuid.NewString(), UserID: userID, ProjectID: storetest.SeedProjectID, Name: "t-" + uuid.NewString()[:6],
			Kind: core.TriggerKindIntegration, Enabled: enabled, Workflow: "builtin://agent", Message: "go",
			DaemonID: "d", Config: cfg, CreatedAt: now, UpdatedAt: now,
		}))
	}
	mk("bob", "github", true)
	mk("alice", "github", true)
	mk("alice", "github", true)
	mk("carol", "github", false)
	mk("dave", "slack", true)

	owners, err := store.ListIntegrationTriggerOwners(ctx, env.ORM, "github")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, owners, "distinct, sorted, enabled triggers of the integration only")
	twin, err := env.Repo.ListIntegrationTriggerOwners(ctx, "github")
	require.NoError(t, err)
	assert.Equal(t, owners, twin)
	none, err := store.ListIntegrationTriggerOwners(ctx, env.ORM, "linear")
	require.NoError(t, err)
	assert.NotNil(t, none)
	assert.Empty(t, none)
}

func ledgerWrite(id, scope string, created time.Time) *core.IntegrationWrite {
	return &core.IntegrationWrite{
		ID: id, UserID: "alice", ChatID: "chat-1", WorkflowName: "wf", NodeID: "n", ToolCallID: "tc",
		Integration: "github", Action: "create_issue", Scope: scope, Target: scope,
		Correlation: map[string]any{"object_kind": "issue", "n": float64(1)}, CreatedAt: created,
	}
}

func TestIntegrationWriteLedger(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	ledger := env.Repo.IntegrationWrites()

	// Defaults: an empty status is an intent, an empty actor the owner.
	require.NoError(t, store.InsertIntegrationWrite(ctx, env.ORM, ledgerWrite("w1", "github:acme/app", t0)))
	row, err := store.GetIntegrationWriteByID(ctx, env.ORM, "w1")
	require.NoError(t, err)
	assert.Equal(t, core.IntegrationWriteIntent, row.Status)
	assert.Equal(t, core.IntegrationActorOwner, row.Actor)
	require.NoError(t, ledger.CreateIntegrationWrite(ctx, ledgerWrite("w2", "github:acme/app", t0.Add(time.Second))))
	failed := ledgerWrite("w3", "github:acme/app", t0.Add(2*time.Second))
	failed.Status, failed.Actor = core.IntegrationWriteFailed, core.IntegrationActorApp
	require.NoError(t, store.InsertIntegrationWrite(ctx, env.ORM, failed))
	ambiguous := ledgerWrite("w4", "github:acme/app", t0.Add(3*time.Second))
	ambiguous.Status, ambiguous.Ambiguous = core.IntegrationWriteFailed, true
	require.NoError(t, store.InsertIntegrationWrite(ctx, env.ORM, ambiguous))
	require.NoError(t, store.InsertIntegrationWrite(ctx, env.ORM, ledgerWrite("w5", "github:other/repo", t0)))

	// Complete settles an intent once; the target is kept when not given.
	done := t0.Add(time.Minute)
	ok, err := store.CompleteIntegrationWrite(ctx, env.ORM, "w1", core.IntegrationWriteResult{
		Status: core.IntegrationWriteDone, ObjectID: "42", ObjectURL: "https://x/42", ConnectionID: "c1",
	}, done)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = ledger.CompleteIntegrationWrite(ctx, "w1", core.IntegrationWriteResult{Status: core.IntegrationWriteFailed})
	require.NoError(t, err)
	assert.False(t, ok, "a settled entry is never rewritten")
	ok, err = ledger.CompleteIntegrationWrite(ctx, "w2", core.IntegrationWriteResult{
		Status: core.IntegrationWriteDone, Target: "github:acme/app#7", ObjectID: "7", At: done,
	})
	require.NoError(t, err)
	assert.True(t, ok)

	w1, err := ledger.GetIntegrationWriteByObject(ctx, "github", "github:acme/app", "issue", "42")
	require.NoError(t, err)
	assert.Equal(t, "w1", w1.ID)
	assert.Equal(t, "github:acme/app", w1.Target, "an empty result target keeps the intent's")
	assert.Equal(t, "https://x/42", w1.ObjectURL)
	assert.Equal(t, map[string]any{"object_kind": "issue", "n": float64(1)}, w1.Correlation)
	require.NotNil(t, w1.CompletedAt)
	assert.True(t, w1.CompletedAt.Equal(done))
	w2, err := store.GetIntegrationWriteByObject(ctx, env.ORM, "github", "github:acme/app", "issue", "7")
	require.NoError(t, err)
	assert.Equal(t, "github:acme/app#7", w2.Target)
	_, err = store.GetIntegrationWriteByObject(ctx, env.ORM, "github", "github:acme/app", "pull_request", "42")
	assert.ErrorIs(t, err, core.ErrIntegrationWriteNotFound, "the object kind is part of the key")

	// Recent: newest first, a definite failure excluded, an ambiguous one kept.
	recent, err := store.ListRecentIntegrationWrites(ctx, env.ORM, "github", "github:acme/app", t0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"w4", "w2", "w1"}, writeIDs(recent))
	recent, err = ledger.ListRecentIntegrationWrites(ctx, "github", "github:acme/app", t0.Add(time.Second), 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"w4"}, writeIDs(recent))

	n, err := store.PruneIntegrationWrites(ctx, env.ORM, t0.Add(time.Second))
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "w1 and w5 were created before")
	n, err = ledger.PruneIntegrationWrites(ctx, t0.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)
}

// The ledger never joins an ambient transaction: an intent row written
// inside one is committed on its own, so a rollback of the caller's work
// cannot take it, and an event arriving first already sees it.
func TestIntegrationWriteLedgerOutlivesTheCallersTransaction(t *testing.T) {
	env := storetest.New(t)
	ctx := context.Background()
	ledger := env.Repo.IntegrationWrites()
	rollback := errors.New("roll back")

	err := env.Repo.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		if err := ledger.CreateIntegrationWrite(ctx, ledgerWrite("w-tx", "github:acme/app", time.Now().UTC())); err != nil {
			return err
		}
		ok, err := ledger.CompleteIntegrationWrite(ctx, "w-tx", core.IntegrationWriteResult{Status: core.IntegrationWriteDone, ObjectID: "1"})
		if err != nil || !ok {
			return errors.Join(err, errors.New("complete did not settle"))
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	w, err := ledger.GetIntegrationWriteByObject(ctx, "github", "github:acme/app", "issue", "1")
	require.NoError(t, err, "the entry and its completion survived the caller's rollback")
	assert.Equal(t, "w-tx", w.ID)
}

func writeIDs(ws []*core.IntegrationWrite) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.ID)
	}
	return out
}
