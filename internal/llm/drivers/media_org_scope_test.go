// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/db"
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
// accounts first; the person's own only with their say; never under
// org_only. Without their say a generation only their own accounts could
// make is not made, and says how to allow it. The person's managed key bills
// the org (org-funded); their Gemini key is their own.
func TestMediaSpendsThePersonsOwnAccountsOnlyWithTheirSay(t *testing.T) {
	seedMediaUser(t)
	withProvidersSwitch(t, orgpolicy.EnforcementEnforce)
	image := models.ModelSelector{Tags: []string{mediaImageTag}}

	t.Run("the org's accounts serve first", func(t *testing.T) {
		resolved, _, _, err := resolveMediaModel(inRun(policyCtx(nil), orgScope(true)), policyUser, image, models.ModalityImage)
		require.NoError(t, err)
		assert.Equal(t, "reliant", resolved.Provider.Driver, "org-funded managed credit before the person's own Gemini key")
	})

	// The org's policy leaves its managed credit no image model: only the
	// person's own Gemini key could generate one.
	noManagedImages := policyCtx(&controlplanev1.OrgPolicy{Models: &controlplanev1.ModelsPolicy{Mode: enforced, Allowed: []string{"claude-opus-4-6"}}})
	for name, tc := range map[string]struct {
		funding RunFunding
		want    string // the driver that serves, "" for a refusal
		orgOnly bool
	}{
		"without their say it is not made":         {funding: RunFunding{}},
		"org_only never reaches their accounts":    {funding: RunFunding{OrgOnly: true, PersonalWhenOrgCannot: true}, orgOnly: true},
		"use_personal when the org's cannot":       {funding: RunFunding{PersonalWhenOrgCannot: true}, want: "gemini"},
		"their own accounts first, by their order": {funding: RunFunding{PersonalFirst: true}, want: "gemini"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := inRunFunded(noManagedImages, orgScope(true), tc.funding)
			resolved, _, _, err := resolveMediaModel(ctx, policyUser, image, models.ModalityImage)
			if tc.want != "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, resolved.Provider.Driver)
				assert.NoError(t, CheckMediaAvailable(ctx, policyUser, models.ModalityImage))
				return
			}
			var funding *models.MediaPersonalFundingError
			require.True(t, errors.As(err, &funding), "want the funding refusal, got %v", err)
			assert.Equal(t, tc.orgOnly, funding.OrgOnly)
			assert.Contains(t, funding.Message, "not a failure to retry")
			_, err = ResolveImageGenerator(ctx, policyUser, image)
			assert.True(t, errors.As(err, &funding), "the generator is never built: %v", err)
			assert.True(t, errors.As(CheckMediaAvailable(ctx, policyUser, models.ModalityImage), &funding), "load_tool says so too")
		})
	}

	t.Run("an org that gave the person nothing leaves their own accounts serving", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		t.Cleanup(cleanup)
		InitializeAPIKeyProvider(repo)
		db.SeedProviderAccount(t, repo, policyUser, "gemini", "AIza-"+policyUser)
		resolved, _, _, err := resolveMediaModel(inRun(policyCtx(nil), orgScope(true)), policyUser, image, models.ModalityImage)
		require.NoError(t, err)
		assert.Equal(t, "gemini", resolved.Provider.Driver)

		_, _, _, err = resolveMediaModel(inRunFunded(policyCtx(nil), orgScope(true), RunFunding{OrgOnly: true}), policyUser, image, models.ModalityImage)
		var funding *models.MediaPersonalFundingError
		assert.True(t, errors.As(err, &funding), "org_only holds even then: %v", err)
	})
}
