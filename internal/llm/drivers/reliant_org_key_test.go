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

// orgMinter is control-plane's token store for one person: it records every
// mint, answers each with a fresh rlat_ token (or err), and lists and revokes
// the live tokens as AccessTokenInternalService does.
type orgMinter struct {
	mu      sync.Mutex
	reqs    []accesstokenclient.MintRequest
	err     error
	live    map[string]accesstokenclient.TokenInfo // by id
	revoked []string
	created time.Time
}

func (m *orgMinter) MintForUser(_ context.Context, req accesstokenclient.MintRequest) (accesstokenclient.Minted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, req)
	if m.err != nil {
		return accesstokenclient.Minted{}, m.err
	}
	n := string(rune('a' + len(m.reqs)))
	plain := fat.Prefix + strings.Repeat(n, fat.TokenLen-len(fat.Prefix))
	id := "tok-" + n
	if m.live == nil {
		m.live = map[string]accesstokenclient.TokenInfo{}
	}
	m.live[id] = accesstokenclient.TokenInfo{ID: id, Name: req.Name, DisplayPrefix: plain[:len(fat.Prefix)+fat.DisplayPrefixLen], CreatedAt: m.created}
	return accesstokenclient.Minted{TokenID: id, Plaintext: plain, Rotated: req.Rotate}, nil
}

func (m *orgMinter) ListForUser(context.Context, string, fat.Scope) ([]accesstokenclient.TokenInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]accesstokenclient.TokenInfo, 0, len(m.live))
	for _, t := range m.live {
		out = append(out, t)
	}
	return out, nil
}

func (m *orgMinter) RevokeForUser(_ context.Context, _ string, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, id)
	m.revoked = append(m.revoked, id)
	return nil
}

// isLive reports whether the token whose plaintext is key is live.
func (m *orgMinter) isLive(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.live {
		if strings.HasPrefix(key, t.DisplayPrefix) {
			return true
		}
	}
	return false
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
			UserID: "u1", Name: controlplane.ReliantOrgProviderKeyName, Scopes: []fat.Scope{fat.ScopeLLMInvoke}, OrgID: org,
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
		assert.False(t, minter.isLive(before[k].Secrets.Secret), "%s: the replaced key is revoked once the new one is stored", org)
		assert.True(t, minter.isLive(after[k].Secrets.Secret), org)
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

// failingUpserts is the repository with UpsertProviderAccount failing while fail is set.
type failingUpserts struct {
	*db.Repo
	fail bool
}

func (f *failingUpserts) UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error) {
	if f.fail {
		return core.ProviderAccount{}, false, errors.New("database unavailable")
	}
	return f.Repo.UpsertProviderAccount(ctx, u)
}

// B2: a rotation mints the new key, stores it, and only then revokes the old
// one. When the write fails the stored key is not revoked, so it keeps
// serving and the next resolution uses it; the sweep then revokes the token
// the failed write orphaned.
func TestRotateOrgKeys_AFailedWriteLeavesTheStoredKeyServing(t *testing.T) {
	minter := &orgMinter{created: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	store := &failingUpserts{Repo: repo}
	h := NewReliantKeyHealer(store, minter).WithPerOrgKeys(true)
	now := minter.created
	h.now = func() time.Time { return now }
	ctx := context.Background()
	db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)
	stored, _, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)

	store.fail = true
	require.Error(t, h.RotateOrgKeys(ctx, "u1"))
	store.fail = false
	assert.True(t, minter.isLive(stored), "the stored key was not revoked")
	assert.Equal(t, stored, reliantRows(t, repo, "u1")[core.ManagedOrgIdentityKey("org-a")].Secrets.Secret)

	key, minted, err := h.EnsureOrgKey(ctx, "u1", "org-a")
	require.NoError(t, err)
	assert.False(t, minted)
	assert.Equal(t, stored, key, "the next resolution recovers on the stored key")

	mints := minter.mints()
	orphan := mints[len(mints)-1]
	require.Equal(t, "org-a", orphan.OrgID)
	_, err = h.healOrgKeys(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, minter.revoked, 0, "an orphan younger than orphanAge may be another replica's unstored mint")
	now = now.Add(orphanAge)
	_, err = h.healOrgKeys(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, minter.revoked, 1, "the orphaned token is revoked")
	assert.True(t, minter.isLive(stored))
}

// A key the gateway rejected (marked needs-sign-in by the credential
// rejection path) is minted again, by the next resolution and by the sweep,
// and the rejected token is revoked.
func TestEnsureOrgKey_MintsARejectedKeyAgain(t *testing.T) {
	for name, heal := range map[string]func(h *ReliantKeyHealer) error{
		"resolution": func(h *ReliantKeyHealer) error {
			_, _, err := h.EnsureOrgKey(context.Background(), "u1", "org-a")
			return err
		},
		"sweep": func(h *ReliantKeyHealer) error { _, _, err := h.Sweep(context.Background()); return err },
	} {
		t.Run(name, func(t *testing.T) {
			minter := &orgMinter{}
			repo, h := newOrgKeyFixture(t, minter)
			ctx := context.Background()
			db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)
			old, _, err := h.EnsureOrgKey(ctx, "u1", "org-a")
			require.NoError(t, err)
			row := reliantRows(t, repo, "u1")[core.ManagedOrgIdentityKey("org-a")]
			reason := "the gateway rejected the org's managed Reliant key"
			_, err = repo.PatchProviderAccount(ctx, "u1", row.ID, core.AccountPatch{NeedsReauth: &reason})
			require.NoError(t, err)
			accounts, err := repo.ListProviderAccountsWithSecrets(ctx, "u1")
			require.NoError(t, err)
			require.Nil(t, usableOrgReliantKey(accounts, "org-a"), "a rejected key is not usable")

			require.NoError(t, heal(h))
			fresh := reliantRows(t, repo, "u1")[core.ManagedOrgIdentityKey("org-a")]
			assert.NotEqual(t, old, fresh.Secrets.Secret)
			assert.Nil(t, fresh.NeedsReauthAt, "the new key clears the mark")
			assert.Equal(t, row.ID, fresh.ID)
			assert.False(t, minter.isLive(old), "the rejected token is revoked")
		})
	}
}

// B1: a resolution that finds the org's key missing, but another resolution
// mints it first, spends the key the other one stored. Both select it.
func TestLoader_ResolutionsRacingTheMintBothSpendTheOrgsKey(t *testing.T) {
	minter := &orgMinter{}
	repo, h := newOrgKeyFixture(t, minter)
	SetReliantKeyHealer(h)
	t.Cleanup(func() { SetReliantKeyHealer(nil) })
	ctx := context.Background()
	db.SeedProviderAccount(t, repo, "u1", "reliant", validRlat)
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{
		"org-a": {OrgID: "org-a", Member: true, Role: "member", MemberSince: memberSince},
	}}
	before, err := repo.ListProviderAccountsWithSecrets(ctx, "u1")
	require.NoError(t, err)

	// Two resolutions read the accounts before either mints.
	first := &staleOnce{Repo: repo, snapshot: before}
	second := &staleOnce{Repo: repo, snapshot: before}
	scope := Scope{UserID: "u1", OrgID: "org-a", Attended: true}
	for i, r := range []*staleOnce{first, second} {
		avail, err := buildAvailableDriversFor(ctx, r, members, scope, noReportClients)
		require.NoError(t, err)
		require.Len(t, avail.Accounts["reliant"], 1, "resolution %d", i+1)
		assert.Equal(t, "org-a", avail.Accounts["reliant"][0].AttributedOrgID, "resolution %d spends the org's key", i+1)
	}
	assert.Len(t, minter.mints(), 1, "minted once")
}

// staleOnce is the repository whose first read of the person's accounts is the
// snapshot taken before a mint: a resolution that read before another won.
type staleOnce struct {
	*db.Repo
	snapshot []core.AccountWithSecrets
	read     bool
}

func (s *staleOnce) ListProviderAccountsWithSecrets(ctx context.Context, userID string) ([]core.AccountWithSecrets, error) {
	if !s.read {
		s.read = true
		return s.snapshot, nil
	}
	return s.Repo.ListProviderAccountsWithSecrets(ctx, userID)
}
