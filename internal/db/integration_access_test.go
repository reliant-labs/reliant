// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Access-gated routing is a security boundary: an event about a repository
// must reach only triggers whose owner can see that repository. These tests
// pin the database half — what a refresh records, what routing returns, and
// what a revocation removes — against a real Postgres.

func accessTrigger(t *testing.T, repo *Repo, userID, integration string, enabled bool, connectionID *string) *core.Trigger {
	t.Helper()
	projectID := "proj-" + uuid.NewString()[:8]
	createTestTriggerProject(t, repo, projectID, userID)
	cfg, err := json.Marshal(core.IntegrationConfig{Integration: integration, Events: []string{"issues.opened"}})
	require.NoError(t, err)
	now := time.Now().UTC()
	tr := &core.Trigger{
		ID: uuid.NewString(), UserID: userID, ProjectID: projectID, Name: "t-" + uuid.NewString()[:6],
		Kind: core.TriggerKindIntegration, Enabled: enabled, Workflow: "builtin://agent", Message: "go",
		DaemonID: "d", Config: cfg, ConnectionID: connectionID, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateTrigger(context.Background(), tr))
	return tr
}

func routedIDs(t *testing.T, repo *Repo, integration, account, resource string, freshAfter time.Time) []string {
	t.Helper()
	got, err := repo.ListAccessRoutedTriggers(context.Background(), integration, account, resource, freshAfter)
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, tr := range got {
		ids = append(ids, tr.ID)
	}
	return ids
}

func TestAccessRoutingReachesOnlyOwnersWhoCanSeeTheResource(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Alice and Bob are both in installation 100 (one org). Alice can see
	// repos 1 and 2; Bob only repo 1. Carol is in another installation.
	alice := accessTrigger(t, repo, "alice", "github", true, nil)
	bob := accessTrigger(t, repo, "bob", "github", true, nil)
	carol := accessTrigger(t, repo, "carol", "github", true, nil)
	aliceOff := accessTrigger(t, repo, "alice", "github", false, nil)
	aliceSlack := accessTrigger(t, repo, "alice", "slack", true, nil)

	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", now, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1", ResourceLabel: "acme/app"},
		{AccountKey: "100", ResourceKey: "2", ResourceLabel: "acme/secret"},
	}))
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "bob", "github", "22", now, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1", ResourceLabel: "acme/app"},
	}))
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "carol", "github", "33", now, []core.IntegrationAccessGrant{
		{AccountKey: "200", ResourceKey: "9", ResourceLabel: "other/repo"},
	}))

	fresh := now.Add(-time.Minute)
	assert.ElementsMatch(t, []string{alice.ID, bob.ID}, routedIDs(t, repo, "github", "100", "1", fresh),
		"both members who can see repo 1; never the disabled trigger or the other integration's")
	assert.Equal(t, []string{alice.ID}, routedIDs(t, repo, "github", "100", "2", fresh),
		"bob cannot see repo 2, so his trigger never sees its events")
	assert.Equal(t, []string{carol.ID}, routedIDs(t, repo, "github", "200", "9", fresh))
	assert.Empty(t, routedIDs(t, repo, "github", "200", "1", fresh), "a resource id is scoped to its installation")
	assert.Empty(t, routedIDs(t, repo, "github", "100", "1", now.Add(time.Minute)),
		"a snapshot older than the freshness bound routes nothing")
	_ = aliceOff
	_ = aliceSlack
}

func TestReplaceIntegrationAccessDropsWhatTheRefreshNoLongerSees(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	tr := accessTrigger(t, repo, "alice", "github", true, nil)
	t0 := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", t0, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1"}, {AccountKey: "100", ResourceKey: "2"},
	}))
	t1 := t0.Add(30 * time.Minute)
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", t1, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1"},
	}))
	since := t0.Add(-time.Minute)
	assert.Equal(t, []string{tr.ID}, routedIDs(t, repo, "github", "100", "1", since))
	assert.Empty(t, routedIDs(t, repo, "github", "100", "2", since), "lost access is gone, not merely stale")

	// A refresh that finds nothing (the credential is gone) clears it all.
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", t1.Add(time.Minute), nil))
	assert.Empty(t, routedIDs(t, repo, "github", "100", "1", since))
}

// A trigger that names a saved connection routes only while that connection
// is its owner's and active. One with no connection (a delegated authority)
// routes on its owner's access alone.
func TestAccessRoutingHonoursANamedConnection(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	conn := &core.Connection{
		ID: uuid.NewString(), OwnerKind: core.ConnectionOwnerUser, UserID: "alice", IntegrationID: "github",
		AuthKind: "oauth2", Name: "gh", Status: core.ConnectionStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.Connections().CreateConnection(ctx, conn, nil,
		core.ConnectionEvent{ConnectionID: conn.ID, UserID: "alice", Kind: core.ConnectionEventCreated, Actor: "user:alice"}))
	withConn := accessTrigger(t, repo, "alice", "github", true, &conn.ID)
	delegated := accessTrigger(t, repo, "alice", "github", true, nil)
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", now, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1"},
	}))
	fresh := now.Add(-time.Minute)
	assert.ElementsMatch(t, []string{withConn.ID, delegated.ID}, routedIDs(t, repo, "github", "100", "1", fresh))

	_, err := repo.DB.ExecContext(ctx, `UPDATE connections SET status = 'needs_reauth' WHERE id = $1`, conn.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{delegated.ID}, routedIDs(t, repo, "github", "100", "1", fresh),
		"a trigger whose own connection cannot authenticate does not route")
}

func TestRevokeIntegrationAccess(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	alice := accessTrigger(t, repo, "alice", "github", true, nil)
	bob := accessTrigger(t, repo, "bob", "github", true, nil)
	seed := func() {
		require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", now, []core.IntegrationAccessGrant{
			{AccountKey: "100", ResourceKey: "1"}, {AccountKey: "100", ResourceKey: "2"}, {AccountKey: "300", ResourceKey: "5"},
		}))
		require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "bob", "github", "22", now, []core.IntegrationAccessGrant{
			{AccountKey: "100", ResourceKey: "1"},
		}))
	}
	fresh := now.Add(-time.Minute)

	seed()
	n, err := repo.RevokeIntegrationAccess(ctx, "github", core.IntegrationAccessRevocation{AccountKey: "100", ResourceKey: "1"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, n, "a repository removed from the installation, for everyone")
	assert.Empty(t, routedIDs(t, repo, "github", "100", "1", fresh))
	assert.Equal(t, []string{alice.ID}, routedIDs(t, repo, "github", "100", "2", fresh))

	seed()
	_, err = repo.RevokeIntegrationAccess(ctx, "github", core.IntegrationAccessRevocation{SubjectID: "22", AccountKey: "100"})
	require.NoError(t, err)
	assert.Equal(t, []string{alice.ID}, routedIDs(t, repo, "github", "100", "1", fresh), "bob left the org")

	seed()
	_, err = repo.RevokeIntegrationAccess(ctx, "github", core.IntegrationAccessRevocation{AccountKey: "100"})
	require.NoError(t, err)
	assert.Empty(t, routedIDs(t, repo, "github", "100", "1", fresh), "the installation was removed")
	assert.Equal(t, []string{alice.ID}, routedIDs(t, repo, "github", "300", "5", fresh), "other installations are untouched")
	_ = bob

	_, err = repo.RevokeIntegrationAccess(ctx, "github", core.IntegrationAccessRevocation{})
	assert.Error(t, err, "an unscoped revocation would wipe every user's access")
}

func TestIntegrationAccessRefreshLease(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	ok, err := repo.ClaimIntegrationAccessRefresh(ctx, "alice", "github", now, now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.True(t, ok, "never refreshed: due")
	ok, err = repo.ClaimIntegrationAccessRefresh(ctx, "alice", "github", now, now.Add(time.Minute), now)
	require.NoError(t, err)
	assert.False(t, ok, "leased by another replica")

	require.NoError(t, repo.FinishIntegrationAccessRefresh(ctx, "alice", "github", now, nil))
	ok, err = repo.ClaimIntegrationAccessRefresh(ctx, "alice", "github", now.Add(time.Second), now.Add(time.Minute), now.Add(-time.Minute))
	require.NoError(t, err)
	assert.False(t, ok, "attempted after due_before: not due yet")
	ok, err = repo.ClaimIntegrationAccessRefresh(ctx, "alice", "github", now.Add(time.Second), now.Add(time.Minute), now.Add(time.Second))
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, repo.FinishIntegrationAccessRefresh(ctx, "alice", "github", now.Add(2*time.Second), assert.AnError))
	state, err := repo.GetIntegrationAccessRefresh(ctx, "alice", "github")
	require.NoError(t, err)
	assert.Equal(t, assert.AnError.Error(), state.LastError)
	require.NotNil(t, state.RefreshedAt)
	assert.WithinDuration(t, now, *state.RefreshedAt, time.Millisecond, "a failure does not move refreshed_at")

	owners, err := repo.ListIntegrationTriggerOwners(ctx, "github")
	require.NoError(t, err)
	assert.Empty(t, owners)
	accessTrigger(t, repo, "alice", "github", true, nil)
	accessTrigger(t, repo, "alice", "github", true, nil)
	accessTrigger(t, repo, "bob", "github", false, nil)
	owners, err = repo.ListIntegrationTriggerOwners(ctx, "github")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice"}, owners)
}

// Two refreshes of one user can finish out of order. The one that started
// earlier must not roll back what the later one recorded.
func TestReplaceIntegrationAccessIsMonotonic(t *testing.T) {
	repo := NewTestRepo(t)
	ctx := context.Background()
	tr := accessTrigger(t, repo, "alice", "github", true, nil)
	newer := time.Now().UTC()
	older := newer.Add(-time.Minute)
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", newer, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "1"},
	}))
	// The slow, older refresh still saw repo 2 and no longer sees repo 1.
	require.NoError(t, repo.ReplaceIntegrationAccess(ctx, "alice", "github", "11", older, []core.IntegrationAccessGrant{
		{AccountKey: "100", ResourceKey: "2"},
	}))
	assert.Equal(t, []string{tr.ID}, routedIDs(t, repo, "github", "100", "1", newer.Add(-time.Second)),
		"the newer refresh's grant survives, at its own time")
	// repo 2 was inserted fresh (no conflict) at the older time: it exists
	// but is no fresher than that.
	assert.Empty(t, routedIDs(t, repo, "github", "100", "2", newer.Add(-time.Second)))

	n, err := repo.PruneIntegrationAccess(ctx, newer.Add(-time.Second))
	require.NoError(t, err)
	assert.EqualValues(t, 1, n, "the older grant is pruned; the newer one stays")
	assert.Equal(t, []string{tr.ID}, routedIDs(t, repo, "github", "100", "1", older))
}
