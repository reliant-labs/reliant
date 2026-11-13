// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The org half of the loader (control-plane docs/design/org-provider-accounts.md
// §3.3, §4.1, §8.1, §11.2): which accounts a scope may draw on, in which tier,
// and who pays.

// noOrgAccounts is the OrgAccountStore of a fake that has no org accounts.
type noOrgAccounts struct{}

func (noOrgAccounts) ListOwnedProviderAccountsWithSecrets(context.Context, core.AccountOwner) ([]core.AccountWithSecrets, error) {
	return nil, nil
}

func (noOrgAccounts) ListOwnedAccountLimits(context.Context, core.AccountOwner) (map[string]core.LimitState, error) {
	return nil, nil
}

func (noOrgAccounts) ListMemberAccountGrants(context.Context, string, string) (map[string]time.Time, error) {
	return nil, nil
}

// orgFakeRepo is an in-memory account store: the person's rows, each org's
// rows, and grants, read the way the owner-scoped store reads them.
type orgFakeRepo struct {
	user   string
	mine   []core.AccountWithSecrets
	orgs   map[string][]core.AccountWithSecrets
	grants map[string]map[string]time.Time // org -> account -> granted_at (for user)
	limits map[string]map[string]core.LimitState
	reads  []string // the org owners read, in order
}

var _ AvailabilityStore = (*orgFakeRepo)(nil)

func (r *orgFakeRepo) ListProviderAccountsWithSecrets(_ context.Context, userID string) ([]core.AccountWithSecrets, error) {
	if userID != r.user {
		return nil, nil
	}
	return append([]core.AccountWithSecrets(nil), r.mine...), nil
}

func (r *orgFakeRepo) ListAccountLimits(_ context.Context, userID string) (map[string]core.LimitState, error) {
	return r.limits[userID], nil
}

func (r *orgFakeRepo) ListOwnedProviderAccountsWithSecrets(_ context.Context, owner core.AccountOwner) ([]core.AccountWithSecrets, error) {
	r.reads = append(r.reads, owner.Key())
	if !owner.IsOrg() {
		return nil, fmt.Errorf("the loader asked for a person's accounts as an owner: %+v", owner)
	}
	return append([]core.AccountWithSecrets(nil), r.orgs[owner.ID]...), nil
}

func (r *orgFakeRepo) ListOwnedAccountLimits(_ context.Context, owner core.AccountOwner) (map[string]core.LimitState, error) {
	return r.limits[owner.Key()], nil
}

func (r *orgFakeRepo) ListMemberAccountGrants(_ context.Context, orgID, userID string) (map[string]time.Time, error) {
	if userID != r.user {
		return nil, nil
	}
	return r.grants[orgID], nil
}

// fakeMembers answers membership questions from a table.
type fakeMembers struct {
	answer  map[string]accesstokenclient.OrgMembership // by org asked
	ok      bool
	err     error
	defOrg  string
	defErr  error
	defAsks int
}

func (f *fakeMembers) Membership(_ context.Context, _ string, orgID string) (accesstokenclient.OrgMembership, bool, error) {
	if f.err != nil {
		return accesstokenclient.OrgMembership{}, true, f.err
	}
	m, found := f.answer[orgID]
	if !found {
		m = accesstokenclient.OrgMembership{OrgID: orgID}
	}
	return m, f.ok, nil
}

func (f *fakeMembers) DefaultOrg(context.Context, string) (string, error) {
	f.defAsks++
	return f.defOrg, f.defErr
}

var (
	// farFuture is a fixed instant ahead of any run of these tests: a token
	// that expires then is live, a bench that ends then is in force.
	farFuture   = time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	memberSince = time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	afterJoin   = memberSince.Add(time.Hour)
	beforeJoin  = memberSince.Add(-time.Hour)
)

func personalKey(id, user, provider string) core.AccountWithSecrets {
	return core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: id, UserID: user, OwnerKind: core.AccountOwnerUser, Provider: provider, AuthKind: core.AuthKindAPIKey},
		Secrets:         core.AccountSecrets{Secret: "sk-" + id},
	}
}

func claudeSignIn(id, user, attributedOrg string) core.AccountWithSecrets {
	a := core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: id, UserID: user, OwnerKind: core.AccountOwnerUser, Provider: "claude",
			AuthKind: core.AuthKindOAuth, ExpiresAt: farFuture},
		Secrets: core.AccountSecrets{Secret: "sk-ant-oat01-" + id, Refresh: "r-" + id},
	}
	if attributedOrg != "" {
		a.AttributedOrgID, a.AttributionSource = attributedOrg, core.AttributionWorkspace
	}
	return a
}

func managedKey(id, user, attributedOrg string) core.AccountWithSecrets {
	a := core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: id, UserID: user, OwnerKind: core.AccountOwnerUser, Provider: "reliant",
			AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey},
		Secrets: core.AccountSecrets{Secret: validRlat},
	}
	if attributedOrg != "" {
		a.IdentityKey = "managed:" + attributedOrg
		a.AttributedOrgID, a.AttributionSource = attributedOrg, core.AttributionManaged
	}
	return a
}

func orgKey(id, org, access string, priority int) core.AccountWithSecrets {
	return core.AccountWithSecrets{
		ProviderAccount: core.ProviderAccount{ID: id, UserID: core.OrgOwnerKeyPrefix + org, OwnerKind: core.AccountOwnerOrg,
			OrgID: org, OrgAccess: access, Provider: "anthropic", AuthKind: core.AuthKindAPIKey, Priority: priority},
		Secrets: core.AccountSecrets{Secret: "sk-ant-api03-" + id},
	}
}

func loaded(t *testing.T, repo AvailabilityStore, members OrgMembership, scope Scope) models.AvailableDrivers {
	t.Helper()
	avail, err := buildAvailableDriversFor(context.Background(), repo, members, scope, noReportClients)
	require.NoError(t, err)
	return avail
}

func loadedIDs(avail models.AvailableDrivers) []string {
	ids := accountIDs(avail)
	if ids == nil {
		ids = []string{}
	}
	return ids
}

// orgMatrixRepo is person u with a personal key, a seat attributed to X and
// one attributed to Y, and orgs X (one key per audience) and Y.
func orgMatrixRepo() *orgFakeRepo {
	return &orgFakeRepo{
		user: "u",
		mine: []core.AccountWithSecrets{
			personalKey("personal", "u", "openai"),
			claudeSignIn("seat-x", "u", "org-x"),
			claudeSignIn("seat-y", "u", "org-y"),
		},
		orgs: map[string][]core.AccountWithSecrets{
			"org-x": {
				orgKey("x-members", "org-x", core.OrgAccessMembers, 0),
				orgKey("x-admins", "org-x", core.OrgAccessAdmins, 1),
				orgKey("x-selected-granted", "org-x", core.OrgAccessSelected, 2),
				orgKey("x-selected-not", "org-x", core.OrgAccessSelected, 3),
				orgKey("x-selected-before-rejoin", "org-x", core.OrgAccessSelected, 4),
			},
			"org-y": {orgKey("y-members", "org-y", core.OrgAccessMembers, 0)},
		},
		grants: map[string]map[string]time.Time{
			"org-x": {"x-selected-granted": afterJoin, "x-selected-before-rejoin": beforeJoin},
			"org-y": {"y-members": afterJoin},
		},
	}
}

// §8.1's table: {member, admin, removed, non-member, member of another org,
// outage, old control plane, no orgs} × {members, admins, selected with a
// grant, selected without, a grant from before a rejoin} × {org key of X, of
// Y, seat of X, of Y, personal}, in a run of org X.
func TestLoader_OrgAccessMatrix(t *testing.T) {
	t.Parallel()
	member := func(role string, since time.Time) map[string]accesstokenclient.OrgMembership {
		return map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x", Member: true, Role: role, MemberSince: since}}
	}
	cases := []struct {
		name    string
		members OrgMembership
		want    []string
	}{
		{"member", &fakeMembers{ok: true, answer: member("member", memberSince)},
			[]string{"personal", "seat-x", "x-members", "x-selected-granted"}},
		{"admin", &fakeMembers{ok: true, answer: member("admin", memberSince)},
			[]string{"personal", "seat-x", "x-admins", "x-members", "x-selected-granted"}},
		{"owner", &fakeMembers{ok: true, answer: member("owner", memberSince)},
			[]string{"personal", "seat-x", "x-admins", "x-members", "x-selected-granted"}},
		{"old control plane: member with no member_since counts no grant", &fakeMembers{ok: true, answer: member("member", time.Time{})},
			[]string{"personal", "seat-x", "x-members"}},
		{"old control plane: member with no role is not an admin", &fakeMembers{ok: true, answer: member("", memberSince)},
			[]string{"personal", "seat-x", "x-members", "x-selected-granted"}},
		{"removed member", &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x"}}},
			[]string{"personal"}},
		{"non-member", &fakeMembers{ok: true}, []string{"personal"}},
		{"an answer about another org", &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{
			"org-x": {OrgID: "org-y", Member: true, Role: "owner", MemberSince: memberSince}}}, []string{"personal"}},
		{"membership outage fails closed", &fakeMembers{ok: true, err: errors.New("control plane down")}, []string{"personal"}},
		{"a deployment with no orgs", &fakeMembers{ok: false, answer: member("owner", memberSince)}, []string{"personal"}},
		{"no membership resolver wired", nil, []string{"personal"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			avail := loaded(t, orgMatrixRepo(), tc.members, Scope{UserID: "u", OrgID: "org-x", Attended: true})
			assert.Equal(t, tc.want, loadedIDs(avail))
		})
	}
}

// A call that names no org, or names Y, never reaches X's accounts or X's
// seat; a personal scope never reaches any seat.
func TestLoader_OtherScopesNeverReachOrgX(t *testing.T) {
	t.Parallel()
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{
		"org-x": {OrgID: "org-x", Member: true, Role: "owner", MemberSince: memberSince},
		"org-y": {OrgID: "org-y", Member: true, Role: "member", MemberSince: memberSince},
	}}
	repo := orgMatrixRepo()
	assert.Equal(t, []string{"personal"}, loadedIDs(loaded(t, repo, members, PersonalScope("u"))),
		"a personal scope: the person's own unattributed accounts only")
	assert.Empty(t, repo.reads, "a personal scope reads no org's accounts")

	repo = orgMatrixRepo()
	assert.Equal(t, []string{"personal", "seat-y", "y-members"}, loadedIDs(loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-y", Attended: true})))
	assert.Equal(t, []string{"org:org-y"}, repo.reads, "the read names the scope's org and no other")
}

// Tiers (§4.2): the org-paid accounts come first, a seat before an org key,
// each in its owner's order; the person's own after. Funding names who pays.
func TestLoader_TiersFundingAndOrder(t *testing.T) {
	t.Parallel()
	repo := &orgFakeRepo{
		user: "u",
		mine: []core.AccountWithSecrets{
			personalKey("my-anthropic", "u", "anthropic"),
			claudeSignIn("my-claude", "u", ""),
			claudeSignIn("seat", "u", "org-x"),
			managedKey("managed", "u", ""),
		},
		orgs: map[string][]core.AccountWithSecrets{"org-x": {
			orgKey("org-key-2", "org-x", core.OrgAccessMembers, 1),
			orgKey("org-key-1", "org-x", core.OrgAccessMembers, 0),
		}},
	}
	// The store returns an org's rows in priority order.
	repo.orgs["org-x"][0], repo.orgs["org-x"][1] = repo.orgs["org-x"][1], repo.orgs["org-x"][0]
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x", Member: true, Role: "member"}}}
	avail := loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-x", Attended: true})

	byID := map[string]models.AccountConfig{}
	for _, list := range avail.Accounts {
		for _, a := range list {
			byID[a.AccountID] = a
		}
	}
	for id, want := range map[string]struct {
		tier    models.AccountTier
		funding string
	}{
		"org-key-1":    {models.TierFirst, models.FundingOrgKey},
		"org-key-2":    {models.TierFirst, models.FundingOrgKey},
		"seat":         {models.TierFirst, models.FundingOrgSeat},
		"my-anthropic": {models.TierSecond, models.FundingPersonal},
		"my-claude":    {models.TierSecond, models.FundingPersonal},
		"managed":      {models.TierSecond, models.FundingOrgWallet}, // D4: today's place
	} {
		require.Contains(t, byID, id)
		assert.Equal(t, want.tier, byID[id].Tier, id)
		assert.Equal(t, want.funding, byID[id].Funding, id)
	}
	assert.Equal(t, "org-x", byID["org-key-1"].AccountOrgID)
	assert.Equal(t, "org-x", byID["seat"].AttributedOrgID)

	ids := func(d models.DriverID) []string {
		var out []string
		for _, a := range avail.Accounts[d] {
			out = append(out, a.AccountID)
		}
		return out
	}
	assert.Equal(t, []string{"org-key-1", "org-key-2", "my-anthropic"}, ids("anthropic"), "the org tier first, in the admin's order")
	assert.Equal(t, []string{"seat", "my-claude"}, ids("claude"), "the seat before the person's own sign-in")
	assert.Equal(t, "org-key-1", avail.Drivers["anthropic"].AccountID, "the default account is the best tier's")
	assert.Equal(t, map[models.DriverID]models.AccountTier{
		"anthropic": models.TierFirst, "claude": models.TierFirst, "reliant": models.TierSecond,
	}, avail.DriverRank)
}

// With nothing from the org, the loader is what it was: one tier, no rank.
func TestLoader_NothingFromTheOrgChangesNothing(t *testing.T) {
	t.Parallel()
	repo := &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{
		personalKey("my-anthropic", "u", "anthropic"), claudeSignIn("my-claude", "u", ""),
	}}
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x", Member: true, Role: "owner", MemberSince: memberSince}}}
	in := loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-x", Attended: true})
	out := loaded(t, repo, members, PersonalScope("u"))
	assert.Nil(t, in.DriverRank)
	assert.Equal(t, accountIDs(out), accountIDs(in))
	for _, list := range in.Accounts {
		for _, a := range list {
			assert.Equal(t, models.TierSecond, a.Tier)
		}
	}
}

// An org key's limit state is the org's (kept under its owner key), and a
// paused or needs-attention org key is out for every member, naming no
// reason a member could act on.
func TestLoader_OrgKeyLimitsAndStatus(t *testing.T) {
	t.Parallel()
	until := farFuture
	paused := memberSince
	repo := &orgFakeRepo{user: "u",
		orgs: map[string][]core.AccountWithSecrets{"org-x": {
			orgKey("limited", "org-x", core.OrgAccessMembers, 0),
			orgKey("paused", "org-x", core.OrgAccessMembers, 1),
			orgKey("rejected", "org-x", core.OrgAccessMembers, 2),
		}},
		limits: map[string]map[string]core.LimitState{"org:org-x": {"limited": {AccountID: "limited", LimitedUntil: &until}}},
	}
	repo.orgs["org-x"][1].PausedAt = &paused
	repo.orgs["org-x"][2].NeedsReauthAt = &paused
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x", Member: true}}}
	avail := loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-x", Attended: true})
	require.Len(t, avail.Accounts["anthropic"], 1)
	got := avail.Accounts["anthropic"][0]
	assert.Equal(t, "limited", got.AccountID)
	assert.Equal(t, models.AccountLimited, got.Status)
	assert.True(t, until.Equal(got.LimitedUntil))
	assert.Empty(t, avail.Unavailable, "an org key needing attention is the admins' to fix")
}

// §11.2 (I3): an unattended run whose automation is not stamped "any" draws on
// the org's keys and the org's Reliant credits only: never a personal
// account, never the person's org-paid seat. The org-less managed key counts
// only in the person's default org.
func TestLoader_UnattendedScopeIsOrgFundedOnly(t *testing.T) {
	t.Parallel()
	repo := orgMatrixRepo()
	repo.mine = append(repo.mine, managedKey("managed", "u", ""))
	member := map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x", Member: true, Role: "member", MemberSince: memberSince}}
	strict := Scope{UserID: "u", OrgID: "org-x", Attended: false}

	inDefault := &fakeMembers{ok: true, answer: member, defOrg: "org-x"}
	assert.Equal(t, []string{"managed", "x-members", "x-selected-granted"}, loadedIDs(loaded(t, repo, inDefault, strict)))

	elsewhere := &fakeMembers{ok: true, answer: member, defOrg: "org-home"}
	assert.Equal(t, []string{"x-members", "x-selected-granted"}, loadedIDs(loaded(t, repo, elsewhere, strict)),
		"the org-less managed key bills the default org, so it is not org X's credit")

	outage := &fakeMembers{ok: true, answer: member, defErr: errors.New("down")}
	assert.Equal(t, []string{"x-members", "x-selected-granted"}, loadedIDs(loaded(t, repo, outage, strict)), "fails closed")

	repo.mine = append(repo.mine, managedKey("managed-x", "u", "org-x"))
	assert.Equal(t, []string{"managed-x", "x-members", "x-selected-granted"}, loadedIDs(loaded(t, repo, elsewhere, strict)),
		"the managed key minted in X is X's credit")

	anySource := strict
	anySource.UnattendedAnySource = true
	assert.Equal(t, []string{"managed", "managed-x", "personal", "seat-x", "x-members", "x-selected-granted"},
		loadedIDs(loaded(t, repo, elsewhere, anySource)), "an automation stamped any draws on every source, as today")
}

// Property (§8.1, §11.2): over random accounts and scopes, no resolution in a
// scope for org O ever yields an account owned by or attributed to another
// org, and an org-funded-only scope never yields a personal account or a seat.
func TestLoader_PropertyNoCrossOrgAndNoPersonalWhenUnattended(t *testing.T) {
	t.Parallel()
	orgNames := []string{"org-a", "org-b", "org-c"}
	rng := rand.New(rand.NewPCG(1, 2))
	pick := func(xs []string) string { return xs[rng.IntN(len(xs))] }
	accesses := []string{core.OrgAccessMembers, core.OrgAccessAdmins, core.OrgAccessSelected}
	roles := []string{"owner", "admin", "member", ""}

	for i := 0; i < 400; i++ {
		repo := &orgFakeRepo{user: "u", orgs: map[string][]core.AccountWithSecrets{}, grants: map[string]map[string]time.Time{}}
		for j := 0; j < 1+rng.IntN(6); j++ {
			id := fmt.Sprintf("p%d", j)
			switch rng.IntN(4) {
			case 0:
				repo.mine = append(repo.mine, personalKey(id, "u", pick([]string{"anthropic", "openai"})))
			case 1:
				attributed := ""
				if rng.IntN(2) == 0 {
					attributed = pick(orgNames)
				}
				repo.mine = append(repo.mine, claudeSignIn(id, "u", attributed))
			case 2:
				attributed := ""
				if rng.IntN(2) == 0 {
					attributed = pick(orgNames)
				}
				repo.mine = append(repo.mine, managedKey(id, "u", attributed))
			}
		}
		for _, org := range orgNames {
			repo.grants[org] = map[string]time.Time{}
			for j := 0; j < rng.IntN(4); j++ {
				id := fmt.Sprintf("%s-k%d", org, j)
				repo.orgs[org] = append(repo.orgs[org], orgKey(id, org, accesses[rng.IntN(3)], j))
				if rng.IntN(2) == 0 {
					repo.grants[org][id] = []time.Time{beforeJoin, afterJoin}[rng.IntN(2)]
				}
			}
		}
		answer := map[string]accesstokenclient.OrgMembership{}
		for _, org := range orgNames {
			if rng.IntN(3) > 0 {
				answer[org] = accesstokenclient.OrgMembership{OrgID: org, Member: true, Role: roles[rng.IntN(len(roles))], MemberSince: memberSince}
			}
		}
		members := &fakeMembers{ok: true, answer: answer, defOrg: pick(orgNames)}
		scope := Scope{UserID: "u", OrgID: pick(append([]string{""}, orgNames...)), Attended: rng.IntN(2) == 0, UnattendedAnySource: rng.IntN(3) == 0}

		avail := loaded(t, repo, members, scope)
		for _, list := range avail.Accounts {
			for _, a := range list {
				for _, org := range []string{a.AccountOrgID, a.AttributedOrgID} {
					require.True(t, org == "" || org == scope.OrgID, "scope %+v yielded %s of org %s", scope, a.AccountID, org)
				}
				if scope.Personal() {
					require.Empty(t, a.AttributedOrgID, "a personal scope yielded %s", a.AccountID)
				}
				if scope.OrgFundedOnly() {
					require.True(t, models.OrgFunded(a.Funding), "an unattended scope %+v yielded %s funded %q", scope, a.AccountID, a.Funding)
					require.NotEqual(t, models.FundingOrgSeat, a.Funding, "an unattended scope yielded the seat %s", a.AccountID)
				}
				if a.AccountOrgID != "" {
					_, admitted := answer[scope.OrgID]
					require.True(t, admitted, "%s served a non-member", a.AccountID)
				}
			}
		}
	}
}

// The driver build refuses an account owned by, or attributed to, another org
// than the run's (§8.1 point 3).
func TestCheckAccountScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	x := Scope{UserID: "u", OrgID: "org-x", Attended: true}
	assert.NoError(t, CheckAccountScope(ctx, x, models.DriverConfig{AccountID: "a"}))
	assert.NoError(t, CheckAccountScope(ctx, x, models.DriverConfig{AccountID: "a", AccountOrgID: "org-x"}))
	assert.NoError(t, CheckAccountScope(ctx, x, models.DriverConfig{AccountID: "a", AttributedOrgID: "org-x"}))
	var refused *ErrAccountOutOfScope
	require.ErrorAs(t, CheckAccountScope(ctx, x, models.DriverConfig{AccountID: "a", AccountOrgID: "org-y"}), &refused)
	assert.Equal(t, "org-y", refused.AccountOrg)
	require.ErrorAs(t, CheckAccountScope(ctx, x, models.DriverConfig{AccountID: "a", AttributedOrgID: "org-y"}), &refused)
	require.ErrorAs(t, CheckAccountScope(ctx, PersonalScope("u"), models.DriverConfig{AccountID: "a", AttributedOrgID: "org-x"}), &refused,
		"a seat never serves a call that names no org")
}

// PersonalFirst puts the person's own accounts first and the org-paid ones
// second; the managed key keeps its place; nothing tiered, nothing changes.
func TestPersonalFirst(t *testing.T) {
	t.Parallel()
	repo := &orgFakeRepo{
		user: "u",
		mine: []core.AccountWithSecrets{claudeSignIn("seat", "u", "org-x"), claudeSignIn("mine", "u", ""), managedKey("managed", "u", "")},
		orgs: map[string][]core.AccountWithSecrets{"org-x": {orgKey("org-key", "org-x", core.OrgAccessMembers, 0)}},
	}
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{"org-x": {OrgID: "org-x", Member: true}}}
	avail := loaded(t, repo, members, Scope{UserID: "u", OrgID: "org-x", Attended: true})
	flipped := PersonalFirst(avail)

	assert.Equal(t, "mine", flipped.Drivers["claude"].AccountID)
	assert.Equal(t, models.TierFirst, flipped.Accounts["claude"][0].Tier)
	assert.Equal(t, "seat", flipped.Accounts["claude"][1].AccountID)
	assert.Equal(t, models.TierSecond, flipped.Accounts["claude"][1].Tier)
	assert.Equal(t, models.TierSecond, flipped.Accounts["anthropic"][0].Tier)
	assert.Equal(t, models.TierSecond, flipped.Accounts["reliant"][0].Tier)
	assert.Equal(t, models.TierFirst, flipped.DriverRank["claude"])
	assert.Equal(t, "seat", avail.Drivers["claude"].AccountID, "the input is not modified")

	plain := loaded(t, &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{claudeSignIn("mine", "u", "")}}, members, PersonalScope("u"))
	assert.Equal(t, plain, PersonalFirst(plain))
}
