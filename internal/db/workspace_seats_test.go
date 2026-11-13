// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The store half of the binding matcher (org-provider-accounts §3.4, §6): a sign-in in a bound
// workspace is attributed to the binding's org only while the binding exists, only if it is
// still in that workspace and unattributed, and never once the org released it; unbinding forgets
// the releases, so binding again starts afresh.
func TestWorkspaceSeatAttribution(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	attribution := func(id string) (org, source string) {
		t.Helper()
		var o, s *string
		require.NoError(t, raw.QueryRowContext(ctx,
			`SELECT attributed_org_id, attribution_source FROM provider_accounts WHERE id=$1`, id).Scan(&o, &s))
		if o != nil {
			org = *o
		}
		if s != nil {
			source = *s
		}
		return org, source
	}
	dana := mustUpsert(t, repo, claudeUpsert("dana", "dana", "ws-a", "t", "r"))
	eve := mustUpsert(t, repo, claudeUpsert("eve", "eve", "ws-a", "t", "r"))
	fayElsewhere := mustUpsert(t, repo, claudeUpsert("fay", "fay", "ws-b", "t", "r"))
	key, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: "gus", Provider: "claude", AuthKind: core.AuthKindAPIKey, IdentityKey: "apikey:gus",
		Secrets: core.AccountSecrets{Secret: "sk-ant-gus-0001"},
	})
	require.NoError(t, err)

	ws := core.OrgProviderWorkspace{OrgID: "org-1", Provider: "claude", ExternalOrgID: "ws-a", DisplayName: "A", BoundByUserID: "ada"}

	// No binding yet: nothing is found, and nothing is attributed.
	b, err := repo.ProviderWorkspaceBinding(ctx, "claude", "ws-a")
	require.NoError(t, err)
	assert.Nil(t, b)
	done, err := repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	assert.False(t, done, "no binding, no seat")

	_, _, err = repo.BindOrgProviderWorkspace(ctx, ws)
	require.NoError(t, err)
	b, err = repo.ProviderWorkspaceBinding(ctx, "claude", "ws-a")
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "org-1", b.OrgID)
	none, err := repo.ProviderWorkspaceBinding(ctx, "codex", "ws-a")
	require.NoError(t, err)
	assert.Nil(t, none, "the same id at another provider is another workspace")

	signIns, err := repo.ListWorkspaceSignIns(ctx, "claude", "ws-a")
	require.NoError(t, err)
	var ids []string
	for _, a := range signIns {
		ids = append(ids, a.ID)
	}
	assert.ElementsMatch(t, []string{dana.ID, eve.ID}, ids, "only sign-ins in the workspace; never an API key")

	done, err = repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	assert.True(t, done)
	org, source := attribution(dana.ID)
	assert.Equal(t, "org-1", org)
	assert.Equal(t, core.AttributionWorkspace, source)
	done, err = repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	assert.False(t, done, "already the org's seat")
	for _, id := range []string{fayElsewhere.ID, key.ID} {
		done, err = repo.AttributeWorkspaceSeat(ctx, ws, id)
		require.NoError(t, err)
		assert.False(t, done, "%s is not a sign-in in the workspace", id)
	}

	// A released seat is never matched to the org again; clearing a seat is its own write.
	require.NoError(t, repo.ReleaseOrgSeat(ctx, "org-1", dana.ID, "ada"))
	org, _ = attribution(dana.ID)
	assert.Empty(t, org)
	done, err = repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	assert.False(t, done, "the org released it")

	done, err = repo.AttributeWorkspaceSeat(ctx, ws, eve.ID)
	require.NoError(t, err)
	require.True(t, done)
	cleared, err := repo.ClearWorkspaceSeat(ctx, "org-2", eve.ID)
	require.NoError(t, err)
	assert.False(t, cleared, "another org's clear touches nothing")
	cleared, err = repo.ClearWorkspaceSeat(ctx, "org-1", eve.ID)
	require.NoError(t, err)
	assert.False(t, cleared, "the org's binding still covers it: a stale decision to clear is refused")
	org, _ = attribution(eve.ID)
	assert.Equal(t, "org-1", org)

	// Unbinding forgets the org's releases in the workspace: binding again starts afresh.
	require.NoError(t, repo.UnbindOrgProviderWorkspace(ctx, "org-1", "claude", "ws-a"))
	var releases int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM org_seat_releases`).Scan(&releases))
	assert.Zero(t, releases)
	_, _, err = repo.BindOrgProviderWorkspace(ctx, ws)
	require.NoError(t, err)
	done, err = repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	assert.True(t, done, "a rebind matches a seat released under the old binding")
}

// The clear is decided under the account's lock (review of #2698, B1): an unbind, a rebind and
// the rebind's rematch all land between a matcher's read ("no binding covers this seat") and its
// clear, and the delayed clear leaves the new attribution alone. Once the binding is really gone,
// the same clear goes through.
func TestWorkspaceSeatClearIsDecidedAtomically(t *testing.T) {
	t.Parallel()
	repo, raw, cleanup := SetupTestDBWithRawDB(t)
	defer cleanup()
	ctx := context.Background()
	orgOf := func(id string) string {
		t.Helper()
		var o *string
		require.NoError(t, raw.QueryRowContext(ctx, `SELECT attributed_org_id FROM provider_accounts WHERE id=$1`, id).Scan(&o))
		if o == nil {
			return ""
		}
		return *o
	}
	dana := mustUpsert(t, repo, claudeUpsert("dana", "dana", "ws-a", "t", "r"))
	ws := core.OrgProviderWorkspace{OrgID: "org-1", Provider: "claude", ExternalOrgID: "ws-a", DisplayName: "A", BoundByUserID: "ada"}
	_, _, err := repo.BindOrgProviderWorkspace(ctx, ws)
	require.NoError(t, err)
	done, err := repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	require.True(t, done)

	// A matcher read the seat while the workspace was unbound...
	require.NoError(t, repo.UnbindOrgProviderWorkspace(ctx, "org-1", "claude", "ws-a"))
	// ...then the admin bound it again and the rematch attributed the seat anew...
	_, _, err = repo.BindOrgProviderWorkspace(ctx, ws)
	require.NoError(t, err)
	done, err = repo.AttributeWorkspaceSeat(ctx, ws, dana.ID)
	require.NoError(t, err)
	require.True(t, done)
	// ...and only now does the matcher's clear run.
	cleared, err := repo.ClearWorkspaceSeat(ctx, "org-1", dana.ID)
	require.NoError(t, err)
	assert.False(t, cleared)
	assert.Equal(t, "org-1", orgOf(dana.ID), "the new attribution survives the delayed clear")

	// A seat no binding covers (here: its row says org-1, the binding is gone and nothing
	// cleared it) is cleared.
	_, err = raw.ExecContext(ctx, `DELETE FROM org_provider_workspaces WHERE org_id = 'org-1'`)
	require.NoError(t, err)
	cleared, err = repo.ClearWorkspaceSeat(ctx, "org-1", dana.ID)
	require.NoError(t, err)
	assert.True(t, cleared)
	assert.Empty(t, orgOf(dana.ID))
}
