// Copyright (c) 2025 Reliant Labs

package orgaccounts_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Every operation takes the org it acts for, and an id of another org's account, grant, binding or
// seat is not found: org A's admin can neither rotate, regrant, release nor read org B's credential,
// and a rotation reseals under the org's own tenant only.
func TestOrgACannotReachOrgBsCredentials(t *testing.T) {
	t.Parallel()
	repo, raw := newEnv(t)
	ctx := context.Background()
	a := mustUpsert(t, repo, orgKeyUpsert("org-a", "adminA", "acme", "SECRET-OF-ORG-A"))
	b := mustUpsert(t, repo, orgKeyUpsert("org-b", "adminB", "globex", "SECRET-OF-ORG-B"))

	// Org A names org B's account id.
	_, err := repo.RotateOrgAPIKey(ctx, "org-a", b.ID, core.OrgAPIKey{Secret: "HIJACK", IdentityKey: "key:hijack", KeyHint: "jack"})
	assert.ErrorIs(t, err, core.ErrProviderAccountNotFound)
	_, err = repo.SetOrgAccountAccess(ctx, "org-a", b.ID, core.OrgAccessMembers)
	assert.Error(t, err)
	assert.ErrorIs(t, repo.SetOrgAccountGrants(ctx, "org-a", b.ID, []string{"m1"}, "adminA"), core.ErrProviderAccountNotFound)

	// Org B's key is exactly what org B stored.
	got, err := repo.accounts.GetOwnedProviderAccountWithSecrets(ctx, core.OrgAccountOwner("org-b"), b.ID)
	require.NoError(t, err)
	assert.Equal(t, "SECRET-OF-ORG-B", got.Secrets.Secret)
	grantsB, err := repo.ListOrgAccountGrants(ctx, "org-b")
	require.NoError(t, err)
	assert.Empty(t, grantsB, "org A's attempt granted nothing in org B")

	// A rotation of org A's own key seals under org A's tenant, not its admin's, not org B's.
	rotated, err := repo.RotateOrgAPIKey(ctx, "org-a", a.ID, core.OrgAPIKey{Secret: "ROTATED-A", IdentityKey: "key:acme2", KeyHint: "TED-A"})
	require.NoError(t, err)
	assert.Equal(t, a.ID, rotated.ID)
	var sealed []byte
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT secret_sealed FROM provider_accounts WHERE id=$1`, a.ID).Scan(&sealed))
	aad := ProviderAccountAAD(a.ID, accountColSecret)
	plain, err := repo.vault.Open(ctx, orgTenant("org-a"), sealed, aad)
	require.NoError(t, err)
	assert.Equal(t, "ROTATED-A", string(plain))
	for _, wrong := range []string{"org-b"} {
		_, err := repo.vault.Open(ctx, orgTenant(wrong), sealed, aad)
		assert.Error(t, err, "org A's rotated key must not open under %s's tenant", wrong)
	}
}

// Grants, workspace bindings and seats are read and changed for one org only.
func TestOrgGrantsBindingsAndSeatsAreScopedToTheirOrg(t *testing.T) {
	t.Parallel()
	repo, _ := newEnv(t)
	ctx := context.Background()
	a := mustUpsert(t, repo, orgKeyUpsert("org-a", "adminA", "acme", "SECRET-OF-ORG-A"))
	b := mustUpsert(t, repo, orgKeyUpsert("org-b", "adminB", "globex", "SECRET-OF-ORG-B"))
	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-a", a.ID, []string{"m1"}, "adminA"))
	require.NoError(t, repo.SetOrgAccountGrants(ctx, "org-b", b.ID, []string{"m2"}, "adminB"))

	ga, err := repo.ListOrgAccountGrants(ctx, "org-a")
	require.NoError(t, err)
	require.Len(t, ga, 1)
	assert.Equal(t, "m1", ga[0].UserID)
	gb, err := repo.ListOrgAccountGrants(ctx, "org-b")
	require.NoError(t, err)
	require.Len(t, gb, 1)
	assert.Equal(t, "m2", gb[0].UserID)
	gnone, err := repo.ListOrgAccountGrants(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, gnone, "an empty org lists nothing")

	ws := core.OrgProviderWorkspace{OrgID: "org-a", Provider: "claude", ExternalOrgID: "ws-1", DisplayName: "A", BoundByUserID: "adminA"}
	_, _, err = repo.BindOrgProviderWorkspace(ctx, ws)
	require.NoError(t, err)
	// Another org binding the same provider workspace is refused, and cannot unbind org A's.
	_, _, err = repo.BindOrgProviderWorkspace(ctx, core.OrgProviderWorkspace{OrgID: "org-b", Provider: "claude", ExternalOrgID: "ws-1", DisplayName: "B", BoundByUserID: "adminB"})
	assert.ErrorIs(t, err, core.ErrProviderWorkspaceBoundElsewhere)
	assert.ErrorIs(t, repo.UnbindOrgProviderWorkspace(ctx, "org-b", "claude", "ws-1"), core.ErrProviderWorkspaceNotFound)
	wsB, err := repo.ListOrgProviderWorkspaces(ctx, "org-b")
	require.NoError(t, err)
	assert.Empty(t, wsB)
	wsA, err := repo.ListOrgProviderWorkspaces(ctx, "org-a")
	require.NoError(t, err)
	assert.Len(t, wsA, 1)

	// Org B cannot release a seat of org A.
	seat := mustUpsert(t, repo, claudeUpsert("dana", "dana", "ws-1", "t", "r"))
	done, err := repo.AttributeWorkspaceSeat(ctx, ws, seat.ID)
	require.NoError(t, err)
	require.True(t, done)
	assert.ErrorIs(t, repo.ReleaseOrgSeat(ctx, "org-b", seat.ID, "adminB"), core.ErrOrgSeatNotFound)
	seatsB, err := repo.ListOrgSeats(ctx, "org-b")
	require.NoError(t, err)
	assert.Empty(t, seatsB)
	seatsA, err := repo.ListOrgSeats(ctx, "org-a")
	require.NoError(t, err)
	assert.Len(t, seatsA, 1)
}
