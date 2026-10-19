// Copyright (c) 2025 Reliant Labs
package preset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fallbackModel = "claude-5.5-opus@claude"

func TestParsePreset_Providers(t *testing.T) {
	t.Parallel()
	p, err := ParsePreset([]byte(`
name: pinned
tag: agent
params: {}
providers:
  prefer: [codex, "claude:work@acme.com"]
  on_limit: fallback
  fallback: `+fallbackModel+`
`), "pinned")
	require.NoError(t, err)
	require.NotNil(t, p.Providers)
	assert.Equal(t, []string{"codex", "claude:work@acme.com"}, p.Providers.Prefer)
	assert.Equal(t, map[string]interface{}{
		InputKeyProviderPreference: []interface{}{"codex", "claude"},
		InputKeyProviderPolicy: map[string]interface{}{
			"on_limit":          "fallback",
			"fallback_model":    fallbackModel,
			"preferred_account": map[string]interface{}{"claude": "work@acme.com"},
		},
	}, p.Providers.Inputs(), "drivers become the preference; an email reaches preferred_account")
}

func TestParsePreset_ProvidersIsRejectedWhenWrong(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ yaml, wantErr string }{
		"unknown key":                     {"providers:\n  prefr: [codex]\n", `unknown key "prefr"`},
		"unknown provider":                {"providers:\n  prefer: [chatgpt]\n", "unknown provider"},
		"empty instance":                  {"providers:\n  prefer: [\"claude:\"]\n", "empty instance"},
		"local is a model not a provider": {"providers:\n  prefer: [local]\n", "local model"},
		"bad on_limit":                    {"providers:\n  on_limit: retry\n", "not wait or fallback"},
		"fallback without a model":        {"providers:\n  on_limit: fallback\n", "needs providers.fallback"},
		"fallback model without fallback": {"providers:\n  on_limit: wait\n  fallback: " + fallbackModel + "\n", "only meaningful"},
		"fallback with no driver":         {"providers:\n  on_limit: fallback\n  fallback: claude-5.5-opus\n", "<model>@<driver>"},
		"fallback unknown model":          {"providers:\n  on_limit: fallback\n  fallback: no-such-model@codex\n", "invalid model"},
		"not a mapping":                   {"providers: [codex]\n", "must be a mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePreset([]byte("name: x\ntag: agent\nparams: {}\n"+tc.yaml), "x")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestParseProviderRouting_FromSpawnArgs(t *testing.T) {
	t.Parallel()
	r, err := ParseProviderRouting(map[string]interface{}{"prefer": []interface{}{"claude:alice"}, "on_limit": "wait"})
	require.NoError(t, err)
	assert.Equal(t, "alice", r.Inputs()[InputKeyProviderPolicy].(map[string]interface{})["preferred_account"].(map[string]interface{})["claude"])

	r, err = ParseProviderRouting(nil)
	require.NoError(t, err)
	assert.Nil(t, r)
	assert.Nil(t, r.Inputs())

	_, err = ParseProviderRouting(map[string]interface{}{"fallbak": "x"})
	require.Error(t, err)
	_, err = ParseProviderRouting("codex")
	require.Error(t, err)
}

// A preset's providers: land at the top level of a run's inputs whichever group
// the preset targets, and never inside the group's map.
func TestApplyToInputs_ProvidersAreRuntimeInputsAtTheTop(t *testing.T) {
	t.Parallel()
	p := &Preset{Params: map[string]interface{}{"mode": "auto"}, Providers: &ProviderRouting{Prefer: []string{"codex"}, OnLimit: "wait"}}

	flat := ApplyToInputs(p, map[string]interface{}{}, "")
	assert.Equal(t, []interface{}{"codex"}, flat[InputKeyProviderPreference])
	assert.Equal(t, "auto", flat["mode"])

	grouped := ApplyToInputs(p, map[string]interface{}{}, "agent")
	assert.Equal(t, []interface{}{"codex"}, grouped[InputKeyProviderPreference])
	assert.Equal(t, map[string]interface{}{"mode": "auto"}, grouped["agent"], "the group holds params only")

	none := ApplyToInputs(&Preset{Params: map[string]interface{}{"mode": "auto"}}, map[string]interface{}{}, "")
	assert.NotContains(t, none, InputKeyProviderPolicy)
	assert.NotContains(t, none, InputKeyProviderPreference)
}

func TestInputParams_ProvidersRideWithParams(t *testing.T) {
	t.Parallel()
	p := &Preset{Params: map[string]interface{}{"mode": "auto"}, Providers: &ProviderRouting{OnLimit: "wait"}}
	got := p.InputParams()
	assert.Equal(t, "auto", got["mode"])
	assert.Contains(t, got, InputKeyProviderPolicy)
	assert.NotContains(t, p.Params, InputKeyProviderPolicy, "the preset's own params are not mutated")
	assert.Equal(t, map[string]interface{}{"mode": "auto"}, (&Preset{Params: map[string]interface{}{"mode": "auto"}}).InputParams())
}
