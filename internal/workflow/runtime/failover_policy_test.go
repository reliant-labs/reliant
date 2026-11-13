// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

func TestFailoverPolicy(t *testing.T) {
	t.Parallel()
	claude := map[string]interface{}{"model": "claude-5.5-opus@claude"}
	with := func(policy types.RunProviderPolicy) map[string]interface{} {
		in := map[string]interface{}{"model": "claude-5.5-opus@claude"}
		in[InputKeyProviderPolicy] = policy.Value()
		return in
	}

	assert.Equal(t, types.FailoverPolicy{}, failoverPolicy(claude), "a run that opted in to nothing waits")
	assert.Equal(t, types.FailoverPolicy{}, failoverPolicy(nil))
	assert.Equal(t, types.FailoverPolicy{},
		failoverPolicy(with(types.RunProviderPolicy{OnLimit: "wait"})))
	assert.Equal(t, types.FailoverPolicy{FallbackModel: "gpt-5.6-sol@codex"},
		failoverPolicy(with(types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "gpt-5.6-sol@codex"})))
	assert.Equal(t, types.FailoverPolicy{},
		failoverPolicy(with(types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: "claude-5.5-sonnet@claude"})),
		"falling back onto the limited provider is waiting")
	assert.Equal(t, types.FailoverPolicy{PreferredAccount: map[string]string{"claude": "work@acme.com"}},
		failoverPolicy(with(types.RunProviderPolicy{PreferredAccount: map[string]string{"claude": "work@acme.com"}})))
}

// A chat's funding order rides in __provider_policy, so every sub-agent, loop
// body and sub-workflow of the run carries it (org-provider-accounts §4.4); an
// order this release does not know is dropped (the person's setting decides).
func TestFailoverPolicy_FundingIsInherited(t *testing.T) {
	t.Parallel()
	for _, funding := range []string{types.FundingOrgFirst, types.FundingPersonalFirst, types.FundingOrgOnly} {
		in := map[string]interface{}{InputKeyProviderPolicy: types.RunProviderPolicy{Funding: funding}.Value()}
		assert.Equal(t, types.FailoverPolicy{Funding: funding}, failoverPolicy(in))
		// A JSON round trip (Temporal's storage of the input) keeps it.
		roundTripped := types.RunProviderPolicyFromValue(map[string]interface{}{"funding": funding})
		assert.Equal(t, funding, roundTripped.Funding)
		assert.False(t, roundTripped.IsZero())
	}
	assert.Empty(t, types.RunProviderPolicyFromValue(map[string]interface{}{"funding": "free_money"}).Funding)
}
