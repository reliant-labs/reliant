// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The request path over an org's run (control-plane
// docs/design/org-provider-accounts.md §4.3, §8.1): the driver rank decides who
// pays for the model resolution chose, and the driver build never uses an
// account of another org.

func orgAnthropicKey(id, org string) models.AccountConfig {
	return models.AccountConfig{
		DriverConfig: models.DriverConfig{DriverID: "anthropic", AccountID: id, AccountLabel: id, AccountOrgID: org,
			Enabled: true, APIKey: "k-" + id},
		Status: models.AccountAvailable, Tier: models.TierFirst, Funding: models.FundingOrgKey,
	}
}

func TestOrgRouting_TheOrgKeyServesAModelTheSubscriptionAlsoServes(t *testing.T) {
	avail := driversWith(claudeAccount("mine", "me@example.com", 0), orgAnthropicKey("acme-key", "org-acme"))
	avail.DriverRank = map[models.DriverID]models.AccountTier{"anthropic": models.TierFirst, "claude": models.TierSecond}
	withAvailableDrivers(t, avail)
	env := newRoutingEnv(t)

	res, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", OrgID: "org-acme", Attended: true, SessionID: "s", ThreadID: "t1",
		Selector: models.ModelSelector{ID: routeModel}, Routing: env.routing,
	})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", res.ProviderDriver, "the org pays for the model")
	assert.Equal(t, routeModel, res.Definition.ID, "the model is the one asked for")
	require.NotNil(t, res.Account)
	assert.Equal(t, "acme-key", res.Account.Config.AccountID)
	assert.Equal(t, models.FundingOrgKey, usageFunding(res, res.ProviderDriver))
	assert.Equal(t, "org-acme", res.OrgID)

	pinned, err := resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", OrgID: "org-acme", Attended: true, SessionID: "s", ThreadID: "t2",
		Selector: models.ModelSelector{ID: routeModel + "@claude"}, Routing: env.routing,
	})
	require.NoError(t, err)
	assert.Equal(t, "claude", pinned.ProviderDriver, "an explicit pick of the person's provider is honoured")
}

// An account of another org that reaches the driver build (a stale sticky
// row, a bug upstream) is refused, never built.
func TestOrgRouting_TheDriverBuildRefusesAnotherOrgsAccount(t *testing.T) {
	withAvailableDrivers(t, driversWith(orgAnthropicKey("globex-key", "org-globex")))
	env := newRoutingEnv(t)

	_, _ = resolveLLMCall(context.Background(), nil, llmCallSpec{
		UserID: "u1", OrgID: "org-acme", Attended: true, SessionID: "s", ThreadID: "t1",
		Selector: models.ModelSelector{ID: routeModel + "@anthropic"}, Routing: env.routing,
	})
	assert.NotContains(t, env.built, "globex-key", "the routed account of another org was never built")
}
