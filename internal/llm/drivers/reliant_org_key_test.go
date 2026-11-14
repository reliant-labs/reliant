// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// orgMinter records every mint and answers each with a fresh rlat_ token, or err.
type orgMinter struct {
	mu   sync.Mutex
	reqs []accesstokenclient.MintRequest
	err  error
}

func (m *orgMinter) MintForUser(_ context.Context, req accesstokenclient.MintRequest) (accesstokenclient.Minted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, req)
	if m.err != nil {
		return accesstokenclient.Minted{}, m.err
	}
	n := string(rune('a' + len(m.reqs)))
	return accesstokenclient.Minted{Plaintext: fat.Prefix + strings.Repeat(n, fat.TokenLen-len(fat.Prefix)), Rotated: req.Rotate}, nil
}

func (m *orgMinter) mints() []accesstokenclient.MintRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]accesstokenclient.MintRequest(nil), m.reqs...)
}

func reliantRows(t *testing.T, repo *db.Repo, user string) map[string]core.AccountWithSecrets {
	t.Helper()
	accounts, err := repo.ListProviderAccountsWithSecrets(context.Background(), user)
	require.NoError(t, err)
	out := map[string]core.AccountWithSecrets{}
	for _, a := range accounts {
		if a.Provider == "reliant" {
			out[a.IdentityKey] = a
		}
	}
	return out
}

func newOrgKeyFixture(t *testing.T, minter *orgMinter) (*db.Repo, *ReliantKeyHealer) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	return repo, NewReliantKeyHealer(repo, minter).WithPerOrgKeys(true)
}

// A person in two orgs gets one managed key per org (P10): each minted once,
// in that org (MintRequest.OrgID), under the per-org token name so the
// org-less key is never revoked, and stored as managed:<org> attributed to it.
func TestEnsureOrgKey_MintsOneKeyPerOrgInThatOrg(t *testing.T) {
	minter := &orgMinter{}
	repo, h := newOrgKeyFixture(t, minter)
	ctx := context.Background()
	db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)

	keyA, minted, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.True(t, minted)
	keyB, minted, err := h.EnsureOrgKey(ctx, "u1", "org-b")
	require.NoError(t, err)
	assert.True(t, minted)
	again, minted, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.False(t, minted, "held already")
	assert.Equal(t, keyA, again)
	assert.NotEqual(t, keyA, keyB)

	mints := minter.mints()
	require.Len(t, mints, 2)
	for i, org := range []string{"org-a", "org-b"} {
		assert.Equal(t, accesstokenclient.MintRequest{
			UserID: "u1", Name: controlplane.ReliantOrgProviderKeyName, Scopes: []fat.Scope{fat.ScopeLLMInvoke}, Rotate: true, OrgID: org,
		}, mints[i])
	}
	assert.NotEqual(t, controlplane.ReliantProviderKeyName, controlplane.ReliantOrgProviderKeyName,
		"a mint in the default org must not rotate away the org-less key")

	rows := reliantRows(t, repo, "u1")
	require.Len(t, rows, 3)
	assert.Equal(t, validRlat, rows[core.ManagedIdentityKey].Secrets.Secret, "the org-less key is untouched")
	assert.Empty(t, rows[core.ManagedIdentityKey].AttributedOrgID)
	for org, key := range map[string]string{"org-a": keyA, "org-b": keyB} {
		row := rows[core.ManagedOrgIdentityKey(org)]
		assert.Equal(t, key, row.Secrets.Secret)
		assert.Equal(t, org, row.AttributedOrgID)
		assert.Equal(t, core.AttributionManaged, row.AttributionSource)
		assert.Equal(t, core.AuthKindManaged, row.AuthKind)
	}

	// The org-less key's heal reads the org-less row, whatever else is held.
	stored, err := h.readKey(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, validRlat, stored)
}

// Per-org keys off, or a person who does not use Reliant credits: nothing is
// minted.
func TestEnsureOrgKey_MintsNothingOffOrWithoutCredits(t *testing.T) {
	minter := &orgMinter{}
	repo, h := newOrgKeyFixture(t, minter)
	ctx := context.Background()

	_, minted, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.False(t, minted, "no org-less key: the person does not use Reliant credits")

	db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)
	h.WithPerOrgKeys(false)
	_, minted, err = h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.False(t, minted, "per-org keys off")
	assert.Empty(t, minter.mints())
	assert.NoError(t, h.RotateOrgKeys(ctx, "u1"))
	assert.Empty(t, minter.mints())
}

// A refused mint (not a member, an outage) keeps the org-less key and is not
// asked again for that user and org until orgMintRetry has passed.
func TestEnsureOrgKey_AFailedMintIsNotRetriedAtOnce(t *testing.T) {
	minter := &orgMinter{err: errors.New("not a member of org-a")}
	repo, h := newOrgKeyFixture(t, minter)
	ctx := context.Background()
	db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	h.now = func() time.Time { return now }

	_, _, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.Error(t, err)
	_, minted, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.False(t, minted)
	assert.Len(t, minter.mints(), 1, "not asked again at once")
	assert.Len(t, reliantRows(t, repo, "u1"), 1, "only the org-less key")

	now = now.Add(orgMintRetry)
	minter.err = nil
	_, minted, err = h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.True(t, minted, "asked again after the retry window")
}

// A re-sync rotates each org's key in its own org; the org-less key is the
// sync's own.
func TestRotateOrgKeys_RotatesEachOrgsKeyInItsOrg(t *testing.T) {
	minter := &orgMinter{}
	repo, h := newOrgKeyFixture(t, minter)
	ctx := context.Background()
	db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)
	for _, org := range []string{"org-a", "org-b"} {
		_, _, err := h.EnsureOrgKey(ctx, "u1", org)
		require.NoError(t, err)
	}
	before := reliantRows(t, repo, "u1")

	require.NoError(t, h.RotateOrgKeys(ctx, "u1"))
	mints := minter.mints()
	require.Len(t, mints, 4)
	orgs := []string{mints[2].OrgID, mints[3].OrgID}
	assert.ElementsMatch(t, []string{"org-a", "org-b"}, orgs)
	after := reliantRows(t, repo, "u1")
	assert.Len(t, after, 3, "rotated in place, no new rows")
	for _, org := range []string{"org-a", "org-b"} {
		k := core.ManagedOrgIdentityKey(org)
		assert.NotEqual(t, before[k].Secrets.Secret, after[k].Secrets.Secret, org)
		assert.Equal(t, before[k].ID, after[k].ID, org)
	}
	assert.Equal(t, validRlat, after[core.ManagedIdentityKey].Secrets.Secret)
}

// The store accepts a managed key in its two shapes only, and attribution only
// on a managed key minted in an org.
func TestUpsertProviderAccount_ManagedKeyShapes(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	managed := func(identity, org, source string) core.AccountUpsert {
		return core.AccountUpsert{UserID: "u1", Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: identity,
			AttributedOrgID: org, AttributionSource: source, Secrets: core.AccountSecrets{Secret: validRlat}}
	}
	for name, u := range map[string]core.AccountUpsert{
		"org-less": managed(core.ManagedIdentityKey, "", ""),
		"in org-a": managed(core.ManagedOrgIdentityKey("org-a"), "org-a", core.AttributionManaged),
	} {
		_, _, err := repo.UpsertProviderAccount(ctx, u)
		assert.NoError(t, err, name)
	}
	for name, u := range map[string]core.AccountUpsert{
		"another org's identity":      managed(core.ManagedOrgIdentityKey("org-b"), "org-a", core.AttributionManaged),
		"org-less but attributed":     managed(core.ManagedIdentityKey, "org-a", core.AttributionManaged),
		"per-org but unattributed":    managed(core.ManagedOrgIdentityKey("org-a"), "", ""),
		"per-org with a seat source":  managed(core.ManagedOrgIdentityKey("org-a"), "org-a", core.AttributionWorkspace),
		"an API key with attribution": {UserID: "u1", Provider: "openai", AuthKind: core.AuthKindAPIKey, IdentityKey: "k", AttributedOrgID: "org-a", AttributionSource: core.AttributionManaged, Secrets: core.AccountSecrets{Secret: "sk-x"}},
	} {
		_, _, err := repo.UpsertProviderAccount(ctx, u)
		assert.Error(t, err, name)
	}
}
