// Copyright (c) 2025 Reliant Labs
package launch

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/chatselection"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

const (
	claudeOpus = "claude-5.5-opus@claude"
	codexSol   = "gpt-5.6-sol@codex"
)

func claudeRun() map[string]interface{} {
	return map[string]interface{}{"model": map[string]interface{}{"id": claudeOpus}}
}

var defaultsClaudeFallsBack = ProviderPolicyDefaults{
	"claude": {OnLimit: chatselection.OnLimitFallback, FallbackModel: codexSol},
	"codex":  {OnLimit: chatselection.OnLimitWait},
}

func TestParseProviderPolicyDefaults(t *testing.T) {
	t.Parallel()
	got := ParseProviderPolicyDefaults(`{"claude": {"on_limit":"fallback","fallback_model":"gpt-5.6-sol@codex"}, "codex": {"on_limit":"wait"}}`)
	assert.Equal(t, defaultsClaudeFallsBack, got)
	assert.Nil(t, ParseProviderPolicyDefaults(""))
	assert.Nil(t, ParseProviderPolicyDefaults("not json"), "an unreadable default is no default: the default is to wait")
}

// The owner's rule: cross-provider fallback is OFF unless someone turned it on.
func TestEffectiveProviderPolicy(t *testing.T) {
	t.Parallel()
	fallback := chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitFallback, FallbackModel: codexSol}
	wait := chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitWait}
	for name, tc := range map[string]struct {
		inputs   map[string]interface{}
		chat     chatselection.ProviderPolicy
		defaults ProviderPolicyDefaults
		primary  string
		want     types.RunProviderPolicy
	}{
		"nothing set: wait":                               {claudeRun(), chatselection.ProviderPolicy{}, nil, "claude", types.RunProviderPolicy{}},
		"the Settings default applies to its provider":    {claudeRun(), chatselection.ProviderPolicy{}, defaultsClaudeFallsBack, "claude", types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: codexSol}},
		"another provider's default does not apply":       {claudeRun(), chatselection.ProviderPolicy{}, defaultsClaudeFallsBack, "codex", types.RunProviderPolicy{OnLimit: "wait"}},
		"no default for the provider: wait":               {claudeRun(), chatselection.ProviderPolicy{}, defaultsClaudeFallsBack, "gemini", types.RunProviderPolicy{}},
		"the chat's fallback beats a default that waits":  {claudeRun(), fallback, ProviderPolicyDefaults{"claude": {OnLimit: chatselection.OnLimitWait}}, "claude", types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: codexSol}},
		"the chat's wait beats a default that falls back": {claudeRun(), wait, defaultsClaudeFallsBack, "claude", types.RunProviderPolicy{OnLimit: "wait"}},
		"a fallback onto the primary provider is ignored": {
			claudeRun(), chatselection.ProviderPolicy{OnLimit: chatselection.OnLimitFallback, FallbackModel: "claude-5.5-sonnet@claude"}, nil, "claude",
			types.RunProviderPolicy{OnLimit: "wait"},
		},
		"a preset's policy beats the Settings default": {
			map[string]interface{}{"model": map[string]interface{}{"id": claudeOpus}, v2.InputKeyProviderPolicy: types.RunProviderPolicy{OnLimit: "wait"}.Value()},
			chatselection.ProviderPolicy{}, defaultsClaudeFallsBack, "claude", types.RunProviderPolicy{OnLimit: "wait"},
		},
		"the chat's policy beats the preset's": {
			map[string]interface{}{"model": map[string]interface{}{"id": claudeOpus}, v2.InputKeyProviderPolicy: types.RunProviderPolicy{OnLimit: "wait"}.Value()},
			fallback, nil, "claude", types.RunProviderPolicy{OnLimit: "fallback", FallbackModel: codexSol},
		},
		"a preset's preferred account survives the chat's choice": {
			map[string]interface{}{"model": map[string]interface{}{"id": claudeOpus}, v2.InputKeyProviderPolicy: types.RunProviderPolicy{PreferredAccount: map[string]string{"claude": "work@acme.com"}}.Value()},
			wait, nil, "claude", types.RunProviderPolicy{OnLimit: "wait", PreferredAccount: map[string]string{"claude": "work@acme.com"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, EffectiveProviderPolicy(tc.inputs, tc.chat, tc.defaults, tc.primary))
		})
	}
}
