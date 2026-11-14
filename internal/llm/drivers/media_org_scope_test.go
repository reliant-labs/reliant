// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// Image and video generation resolve in the scope of the run the tool call
// belongs to (WithRunScope), as the run's model calls do: the chat's org,
// its accounts, and its policy (control-plane docs/design/org-policies.md
// §4.2, org-provider-accounts.md §4.1). A generation the policy forbids is
// refused with the policy's reason, not "connect a provider".

const mediaImageTag = "image-gen"

// seedMediaUser gives policyUser a Gemini key (image and video) and the
// managed Reliant key, both their own.
func seedMediaUser(t *testing.T) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	InitializeAPIKeyProvider(repo)
	previous := AccountAvailabilityClient
	AccountAvailabilityClient = noReportClients
	t.Cleanup(func() { AccountAvailabilityClient = previous })
	db.SeedProviderAccount(t, repo, policyUser, "gemini", "AIza-"+policyUser)
	db.SeedProviderAccount(t, repo, policyUser, "reliant", policyManagedKey)
}

func inRun(ctx context.Context, scope Scope) context.Context {
	return inRunFunded(ctx, scope, RunFunding{})
}

func inRunFunded(ctx context.Context, scope Scope, funding RunFunding) context.Context {
	return WithRunScope(ctx, scope.UserID, func() (Scope, RunFunding) { return scope, funding })
}

func TestMediaResolvesInTheRunsScope(t *testing.T) {
	seedMediaUser(t)
	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	onlyManaged := policyCtx(onlyDrivers(enforced, "reliant"))

	resolved, _, scope, err := resolveMediaModel(inRun(onlyManaged, orgScope(true)), policyUser, models.ModelSelector{Tags: []string{mediaImageTag}}, models.ModalityImage)
	require.NoError(t, err)
	assert.Equal(t, policyOrg, scope.OrgID, "the run's org, from the context the activity set")
	assert.Equal(t, "reliant", resolved.Provider.Driver, "the org allows only the managed driver: the person's Gemini key is not used")

	_, _, scope, err = resolveMediaModel(onlyManaged, policyUser, models.ModelSelector{Tags: []string{mediaImageTag}}, models.ModalityImage)
	require.NoError(t, err)
	assert.True(t, scope.Personal(), "a call that belongs to no run is the person's own")

	_, _, scope, err = resolveMediaModel(inRun(onlyManaged, orgScope(true)), "someone-else", models.ModelSelector{Tags: []string{mediaImageTag}}, models.ModalityImage)
	assert.True(t, scope.Personal(), "a run's scope is never another person's: %v", err)
}

func TestMediaTheOrgsPolicyForbidsIsRefusedWithItsReason(t *testing.T) {
	seedMediaUser(t)
	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	ctx := inRun(policyCtx(onlyDrivers(enforced, "anthropic")), orgScope(true))

	for name, resolve := range map[string]func() error{
		"image": func() error {
			_, err := ResolveImageGenerator(ctx, policyUser, models.ModelSelector{Tags: []string{mediaImageTag}})
			return err
		},
		"video": func() error {
			_, err := ResolveVideoGenerator(ctx, policyUser, models.ModelSelector{Tags: []string{DefaultVideoGenTag}})
			return err
		},
		"load_tool's check": func() error { return CheckMediaAvailable(ctx, policyUser, models.ModalityImage) },
	} {
		t.Run(name, func(t *testing.T) {
			err := resolve()
			refusal, ok := orgpolicy.RefusalOf(err)
			require.True(t, ok, "want the policy's refusal, got %v", err)
			assert.Equal(t, orgpolicy.ReasonProviderNotAllowed, refusal.Reason)
			assert.False(t, refusal.Unattended, "a person is behind the chat")
			var unavailable *models.MediaUnavailableError
			assert.False(t, errors.As(err, &unavailable), "not \"connect a provider\": the person has one, the policy refuses it")
		})
	}

	// The same person, in no run, keeps their own accounts.
	_, err := ResolveImageGenerator(policyCtx(onlyDrivers(enforced, "anthropic")), policyUser, models.ModelSelector{Tags: []string{mediaImageTag}})
	assert.NoError(t, err)
}

func TestRunScopeIsOnlyTheActorsWhenItSpendsTheActor(t *testing.T) {
	// The activity executes the call as u2, in a chat policyUser owns.
	ctx := WithRunScope(context.Background(), "u2", func() (Scope, RunFunding) { return orgScope(true), RunFunding{PersonalFirst: true} })
	scope, funding := runScopeFor(ctx, "u2")
	assert.Equal(t, PersonalScope("u2"), scope, "a call made as someone other than the chat's owner never gets its org")
	assert.Equal(t, RunFunding{}, funding, "nor the owner's funding choices")
	scope, _ = runScopeFor(ctx, policyUser)
	assert.Equal(t, PersonalScope(policyUser), scope, "nor does the owner's id, when the call executes as someone else")
	scope, _ = runScopeFor(WithRunScope(context.Background(), "", func() (Scope, RunFunding) { return Scope{}, RunFunding{} }), "")
	assert.Equal(t, PersonalScope(""), scope)
}

func TestRunScopeResolvesOnceAndOnlyWhenAsked(t *testing.T) {
	calls := 0
	ctx := WithRunScope(context.Background(), policyUser, func() (Scope, RunFunding) { calls++; return orgScope(true), RunFunding{OrgOnly: true} })
	assert.Zero(t, calls, "a tool call that spends nothing reads nothing")
	for range 2 {
		scope, funding := runScopeFor(ctx, policyUser)
		assert.Equal(t, orgScope(true), scope)
		assert.Equal(t, RunFunding{OrgOnly: true}, funding)
	}
	assert.Equal(t, 1, calls)
	scope, _ := runScopeFor(ctx, "u2")
	assert.Equal(t, PersonalScope("u2"), scope)
	scope, _ = runScopeFor(context.Background(), policyUser)
	assert.Equal(t, PersonalScope(policyUser), scope)
}

// Who may pay for a generation in an org chat follows the person's choice,
// as the chat's model calls do (org-provider-accounts §4.4, §10): the org's
// accounts first; the person's own only with their say, or when the org gives
// them nothing; never under org_only. Without their say a generation only
// their own accounts could make is not made, and says how to allow it.
func TestMediaSpendsThePersonsOwnAccountsOnlyWithTheirSay(t *testing.T) {
	account := func(driver, id, funding string, tier models.AccountTier) models.AccountConfig {
		return models.AccountConfig{
			DriverConfig: models.DriverConfig{DriverID: models.DriverID(driver), APIKey: "key-" + id, Enabled: true, AccountID: id},
			Funding:      funding, Tier: tier,
		}
	}
	candidates := func(orgGave bool, accounts ...models.AccountConfig) models.AvailableDrivers {
		out := models.AvailableDrivers{Drivers: map[models.DriverID]models.DriverConfig{}, Accounts: map[models.DriverID][]models.AccountConfig{}, OrgGave: orgGave}
		for _, a := range accounts {
			out.Accounts[a.DriverID] = append(out.Accounts[a.DriverID], a)
			if _, ok := out.Drivers[a.DriverID]; !ok {
				out.Drivers[a.DriverID] = a.DriverConfig
			}
		}
		return out
	}
	orgKey := account("openai", "org-openai", models.FundingOrgKey, models.TierFirst)
	ownGemini := account("gemini", "own-gemini", models.FundingPersonal, models.TierSecond)
	// The org's key is paused, rejected or refused by its policy: the loader
	// left it out, and still reports that the org gives the person an account.
	orgKeyGone := candidates(true, ownGemini)

	for _, tc := range []struct {
		name    string
		scope   Scope
		funding RunFunding
		all     models.AvailableDrivers
		want    string // the driver that serves; "" for a funding refusal
		orgOnly bool
	}{
		{"the org's accounts serve first", orgScope(true), RunFunding{}, candidates(true, orgKey, ownGemini), "openai", false},
		{"the org's key gone: without their say nothing is spent", orgScope(true), RunFunding{}, orgKeyGone, "", false},
		{"the org's key gone, org_only", orgScope(true), RunFunding{OrgOnly: true, PersonalWhenOrgCannot: true}, orgKeyGone, "", true},
		{"the org's key gone, use_personal when the org's cannot", orgScope(true), RunFunding{PersonalWhenOrgCannot: true}, orgKeyGone, "gemini", false},
		{"their own accounts first, by their order", orgScope(true), RunFunding{PersonalFirst: true}, candidates(true, ownGemini), "gemini", false},
		{"an org that gives nothing leaves their own serving", orgScope(true), RunFunding{}, candidates(false, ownGemini), "gemini", false},
		{"org_only holds even then", orgScope(true), RunFunding{OrgOnly: true}, candidates(false, ownGemini), "", true},
		{"work of no org is the person's own", PersonalScope(policyUser), RunFunding{OrgOnly: true}, candidates(false, ownGemini), "gemini", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, from, err := resolveMediaFrom(context.Background(), policyUser, tc.scope, tc.funding, tc.all,
				models.ModelSelector{Tags: []string{mediaImageTag}}, models.ModalityImage, false)
			if tc.want != "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, resolved.Provider.Driver)
				assert.True(t, from.Drivers[models.DriverID(tc.want)].IsConfigured(), "the set it was resolved from holds the account that serves")
				return
			}
			assert.Nil(t, resolved, "no model, so no generator is built")
			var funding *models.MediaPersonalFundingError
			require.True(t, errors.As(err, &funding), "want the funding refusal, got %v", err)
			assert.Equal(t, tc.orgOnly, funding.OrgOnly)
			assert.Contains(t, funding.Message, "not a failure to retry")
		})
	}
}

// The loader says whether the org gives the person an account before any is
// left out, so an org key the org's policy refuses (or one paused, or
// needing sign-in) is never read as "the org gave nothing".
func TestLoader_OrgGaveCountsAnAccountThatCannotServeNow(t *testing.T) {
	members := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{
		"org-x": {OrgID: "org-x", Member: true, Role: "member", MemberSince: memberSince},
	}}
	repo := func(orgKeys ...core.AccountWithSecrets) *orgFakeRepo {
		return &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{personalKey("own", "u", "gemini")}, orgs: map[string][]core.AccountWithSecrets{"org-x": orgKeys}}
	}
	scope := Scope{UserID: "u", OrgID: "org-x", Attended: true}

	assert.True(t, loaded(t, repo(orgKey("x-key", "org-x", core.OrgAccessMembers, 0)), members, scope).OrgGave)
	assert.False(t, loaded(t, repo(), members, scope).OrgGave, "an org with no account for the person")
	assert.False(t, loaded(t, repo(orgKey("x-key", "org-x", core.OrgAccessMembers, 0)), members, PersonalScope("u")).OrgGave, "work of no org")

	paused := orgKey("x-key", "org-x", core.OrgAccessMembers, 0)
	at := memberSince
	paused.PausedAt = &at
	avail := loaded(t, repo(paused), members, scope)
	assert.True(t, avail.OrgGave, "a paused org key still counts")
	assert.Empty(t, avail.Accounts["anthropic"], "and it serves nothing")

	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	avail, err := buildAvailableDriversFor(policyCtxFor("org-x", onlyDrivers(enforced, "gemini")), repo(orgKey("x-key", "org-x", core.OrgAccessMembers, 0)), members, scope, noReportClients)
	require.NoError(t, err)
	assert.True(t, avail.OrgGave, "an org key the org's policy refuses still counts")
	assert.Empty(t, avail.Accounts["anthropic"])
}

func policyCtxFor(orgID string, doc *controlplanev1.OrgPolicy) context.Context {
	return orgpolicy.WithResolver(context.Background(), orgpolicy.New(policyDocs{docs: map[string]*controlplanev1.OrgPolicy{orgID: doc}}))
}

// failingOrgReads is orgFakeRepo whose reads of the org's accounts, or of the
// person's grants, fail, as an outage does.
type failingOrgReads struct {
	*orgFakeRepo
	accounts, grants bool
}

func (r failingOrgReads) ListOwnedProviderAccountsWithSecrets(ctx context.Context, owner core.AccountOwner) ([]core.AccountWithSecrets, error) {
	if r.accounts {
		return nil, errors.New("database unreachable")
	}
	return r.orgFakeRepo.ListOwnedProviderAccountsWithSecrets(ctx, owner)
}

func (r failingOrgReads) ListMemberAccountGrants(ctx context.Context, orgID, userID string) (map[string]time.Time, error) {
	if r.grants {
		return nil, errors.New("database unreachable")
	}
	return r.orgFakeRepo.ListMemberAccountGrants(ctx, orgID, userID)
}

// What the org gives the person, when nobody could say, is not "nothing": a
// generation never spends their own accounts without their say because a
// read failed.
func TestMediaAnUnknownOrgAnswerNeverSpendsThePersonsOwnAccounts(t *testing.T) {
	member := &fakeMembers{ok: true, answer: map[string]accesstokenclient.OrgMembership{
		"org-x": {OrgID: "org-x", Member: true, Role: "member", MemberSince: memberSince},
	}}
	repo := func() *orgFakeRepo {
		return &orgFakeRepo{user: "u", mine: []core.AccountWithSecrets{personalKey("own", "u", "gemini")},
			orgs: map[string][]core.AccountWithSecrets{"org-x": {orgKey("x-selected", "org-x", core.OrgAccessSelected, 0)}}}
	}
	scope := Scope{UserID: "u", OrgID: "org-x", Attended: true}
	for name, tc := range map[string]struct {
		repo    AvailabilityStore
		members OrgMembership
	}{
		"the org's accounts cannot be read":  {failingOrgReads{orgFakeRepo: repo(), accounts: true}, member},
		"the person's grants cannot be read": {failingOrgReads{orgFakeRepo: repo(), grants: true}, member},
		"the membership cannot be read":      {repo(), &fakeMembers{ok: true, err: errors.New("control plane down")}},
	} {
		t.Run(name, func(t *testing.T) {
			avail := loaded(t, tc.repo, tc.members, scope)
			assert.True(t, avail.OrgGave, "unknown is not \"the org gave nothing\"")
			require.NotEmpty(t, avail.Accounts["gemini"], "the person's own key is still a candidate")

			resolved, _, err := resolveMediaFrom(context.Background(), "u", scope, RunFunding{}, avail,
				models.ModelSelector{Tags: []string{mediaImageTag}}, models.ModalityImage, false)
			assert.Nil(t, resolved, "no generator is built")
			var funding *models.MediaPersonalFundingError
			assert.True(t, errors.As(err, &funding), "want the funding refusal, got %v", err)
		})
	}

	avail := loaded(t, repo(), member, scope)
	assert.False(t, avail.OrgGave, "a definite answer: the person was granted nothing")
}
