// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Writes about an account go to the ACCOUNT's owner, not to the member whose
// request it served (control-plane docs/design/org-provider-accounts.md §3.1,
// §4.7): an org key's limit state, bench and needs-sign-in mark are the org's,
// so they reach every member at once. The acting member here ("u1") is never
// the org.

func orgKeyAccount(id, org string, priority int) models.AccountConfig {
	a := apiKeyAccount(id, id+" (org key)", priority)
	a.AccountOrgID = org
	return a
}

func TestAccountOwner(t *testing.T) {
	t.Parallel()
	assert.Equal(t, core.OrgAccountOwner("org-1"), accountOwner("u1", orgKeyAccount("k", "org-1", 0).DriverConfig))
	assert.Equal(t, core.UserAccountOwner("u1"), accountOwner("u1", apiKeyAccount("k", "me@acme.com", 0).DriverConfig))
	assert.Equal(t, core.UserAccountOwner("u1"), accountOwner("u1", claudeAccount("c", "me@acme.com", 0).DriverConfig))
}

func TestRouting_ObserverNamesTheAccountsOwner(t *testing.T) {
	t.Parallel()
	env := newRoutingEnv(t)
	env.routing.observe(context.Background(), "u1", &routedAccount{Config: orgKeyAccount("org-key", "org-1", 0)})
	env.routing.observe(context.Background(), "u1", &routedAccount{Config: apiKeyAccount("mine", "me@acme.com", 0)})
	assert.Equal(t, []string{"org-key", "mine"}, env.recorder.observed)
	assert.Equal(t, []core.AccountOwner{core.OrgAccountOwner("org-1"), core.UserAccountOwner("u1")}, env.recorder.observedOwners)
}

func TestRouting_ALimitedOrgKeyIsBenchedForTheOrg(t *testing.T) {
	for name, tc := range map[string]struct {
		acct models.AccountConfig
		want core.AccountOwner
	}{
		"org key":          {orgKeyAccount("a", "org-1", 0), core.OrgAccountOwner("org-1")},
		"personal key":     {apiKeyAccount("a", "me@acme.com", 0), core.UserAccountOwner("u1")},
		"personal sign-in": {claudeAccount("a", "me@acme.com", 0), core.UserAccountOwner("u1")},
	} {
		t.Run(name, func(t *testing.T) {
			other := apiKeyAccount("b", "b@acme.com", 1)
			other.DriverID = tc.acct.DriverID
			withAvailableDrivers(t, driversWith(tc.acct, other))
			env := newRoutingEnv(t)
			// The run is org-1's: an org key serves only its own org's runs.
			first, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
				UserID: "u1", OrgID: "org-1", Attended: true, SessionID: "s", ThreadID: "t",
				Selector: models.ModelSelector{ID: routeModel + "@" + string(tc.acct.DriverID)}, Routing: env.routing,
			})
			require.NoError(t, err)
			require.Equal(t, "a", first.Account.Config.AccountID)

			reset := time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)
			refusal := &ratelimit.UsageLimitError{Provider: "Anthropic", StatusCode: 429, ResetAt: reset, Window: "five_hour"}
			got := env.routing.usageLimitFailure(context.Background(), refusal, usageLimitContext{
				Scope: drivers.PersonalScope("u1"), ThreadID: "t", Driver: tc.acct.DriverID, ModelID: routeModel, Account: first.Account,
			})
			var failure *types.UsageLimitFailure
			require.ErrorAs(t, got, &failure)
			assert.Equal(t, []string{"a"}, env.recorder.benched)
			assert.Equal(t, []core.AccountOwner{tc.want}, env.recorder.benchOwners)
		})
	}
}

func TestCredentialRejection_MarksTheOrgsKeyOnTheOrg(t *testing.T) {
	for name, tc := range map[string]struct {
		acct models.AccountConfig
		want core.AccountOwner
	}{
		"org key":      {orgKeyAccount("a", "org-1", 0), core.OrgAccountOwner("org-1")},
		"personal key": {apiKeyAccount("a", "me@acme.com", 0), core.UserAccountOwner("u1")},
	} {
		t.Run(name, func(t *testing.T) {
			withAvailableDrivers(t, driversWith(tc.acct, apiKeyAccount("b", "b@acme.com", 1)))
			env := newRoutingEnv(t)
			marker := &markerFake{store: env.store}
			env.routing.Credentials = marker
			first := resolveOnAnthropic(t, env, "t")
			require.Equal(t, "a", first.Account.Config.AccountID)

			got := env.routing.credentialRejection(context.Background(), rejectedKey(), usageLimitContext{
				Scope: drivers.PersonalScope("u1"), ThreadID: "t", Driver: "anthropic", ModelID: routeModel, Account: first.Account})
			var failure *types.UsageLimitFailure
			require.ErrorAs(t, got, &failure)
			assert.Equal(t, []string{"a"}, marker.marked)
			assert.Equal(t, []core.AccountOwner{tc.want}, marker.owners)
		})
	}
}
