// Copyright (c) 2025 Reliant Labs
package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunProviderPolicyFromValue(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		in   interface{}
		want RunProviderPolicy
	}{
		"nil is the default":                 {nil, RunProviderPolicy{}},
		"a string is the default":            {"fallback", RunProviderPolicy{}},
		"wait":                               {map[string]interface{}{"on_limit": "wait"}, RunProviderPolicy{OnLimit: "wait"}},
		"fallback with a model":              {map[string]interface{}{"on_limit": "fallback", "fallback_model": "m@codex"}, RunProviderPolicy{OnLimit: "fallback", FallbackModel: "m@codex"}},
		"fallback with no model waits":       {map[string]interface{}{"on_limit": "fallback"}, RunProviderPolicy{OnLimit: "wait"}},
		"a model without fallback is moot":   {map[string]interface{}{"on_limit": "wait", "fallback_model": "m@codex"}, RunProviderPolicy{OnLimit: "wait"}},
		"unknown on_limit is the default":    {map[string]interface{}{"on_limit": "explode", "fallback_model": "m@codex"}, RunProviderPolicy{}},
		"accounts alone keep no on_limit":    {map[string]interface{}{"preferred_account": map[string]interface{}{"claude": "work@acme.com"}}, RunProviderPolicy{PreferredAccount: map[string]string{"claude": "work@acme.com"}}},
		"empty account names are dropped":    {map[string]interface{}{"preferred_account": map[string]interface{}{"claude": ""}}, RunProviderPolicy{}},
		"a typed policy is itself":           {RunProviderPolicy{OnLimit: "fallback", FallbackModel: "m@codex"}, RunProviderPolicy{OnLimit: "fallback", FallbackModel: "m@codex"}},
		"a nil typed pointer is the default": {(*RunProviderPolicy)(nil), RunProviderPolicy{}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, RunProviderPolicyFromValue(tc.in))
		})
	}
}

// The policy must read back identically after the JSON round trip a Temporal
// input and a structpb spawn arg put it through.
func TestRunProviderPolicyValueSurvivesJSON(t *testing.T) {
	t.Parallel()
	p := RunProviderPolicy{OnLimit: "fallback", FallbackModel: "m@codex", PreferredAccount: map[string]string{"claude": "work@acme.com"}}
	data, err := json.Marshal(p.Value())
	require.NoError(t, err)
	var back map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, p, RunProviderPolicyFromValue(back))
	assert.True(t, RunProviderPolicy{}.IsZero())
	assert.Empty(t, RunProviderPolicy{}.Value())
}
