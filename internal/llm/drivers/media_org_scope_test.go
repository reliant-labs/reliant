// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	return WithRunScope(ctx, scope.UserID, func() Scope { return scope })
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
	ctx := WithRunScope(context.Background(), "u2", func() Scope { return orgScope(true) })
	assert.Equal(t, PersonalScope("u2"), runScopeFor(ctx, "u2"), "a call made as someone other than the chat's owner never gets its org")
	assert.Equal(t, PersonalScope(policyUser), runScopeFor(ctx, policyUser), "nor does the owner's id, when the call executes as someone else")
	assert.Equal(t, PersonalScope(""), runScopeFor(WithRunScope(context.Background(), "", func() Scope { return Scope{} }), ""))
}

func TestRunScopeResolvesOnceAndOnlyWhenAsked(t *testing.T) {
	calls := 0
	ctx := WithRunScope(context.Background(), policyUser, func() Scope { calls++; return orgScope(true) })
	assert.Zero(t, calls, "a tool call that spends nothing reads nothing")
	assert.Equal(t, orgScope(true), runScopeFor(ctx, policyUser))
	assert.Equal(t, orgScope(true), runScopeFor(ctx, policyUser))
	assert.Equal(t, 1, calls)
	assert.Equal(t, PersonalScope("u2"), runScopeFor(ctx, "u2"))
	assert.Equal(t, PersonalScope(policyUser), runScopeFor(context.Background(), policyUser))
}
