// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tag can declare a thinking default that applies when a request selects a
// model BY that tag. These tests pin the resolution rule, the precedence, and
// the clamp, since all three are invisible at runtime when wrong: the request
// still goes out, just at the wrong effort.

// The embedded catalog's `powerful` tag is the shipping instance of the
// feature. A bare [powerful] selector resolves to fable-5.1 and must come back
// carrying xhigh without the caller ever naming a level.
func TestResolve_PowerfulTagCarriesThinkingDefault(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, allTestProviders)
	require.NoError(t, err)

	assert.Equal(t, "claude-5.1-fable", resolved.Definition.ID)
	assert.Equal(t, "xhigh", resolved.ThinkingLevel)
}

// gemini-3.8-flash is `powerful` but tops out at high, so the tag's xhigh must
// clamp DOWN to high — not error, not ride out to the provider as an
// unsupported value, and not fall back to the model's preferred medium.
func TestResolve_PowerfulTagDefaultClampsToModelCeiling(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(
		ModelSelector{Tags: []string{TagPowerful}},
		[]string{"gemini"},
	)
	require.NoError(t, err)

	require.Equal(t, "gemini-3.8-flash", resolved.Definition.ID)
	require.NotContains(t, resolved.Definition.Capabilities.ThinkingLevels, "xhigh",
		"this case only exercises the clamp while the model tops out below xhigh")
	assert.Equal(t, "high", resolved.ThinkingLevel)
}

// Selecting by explicit id must NEVER pick up a tag default, even for a model
// that carries the tag. The default describes how the model was chosen.
func TestResolve_ByIDDoesNotPickUpTagDefault(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{ID: "claude-5.1-fable"}, allTestProviders)
	require.NoError(t, err)

	require.Contains(t, resolved.Definition.Tags, TagPowerful)
	assert.Empty(t, resolved.ThinkingLevel,
		"id selection must not inherit the powerful tag's thinking default")
}

// A tag with no declared default leaves ThinkingLevel empty, so the consumer
// falls through to the model's own default_thinking_level exactly as before.
func TestResolve_TagWithoutDefaultLeavesThinkingLevelEmpty(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagFlagship}}, allTestProviders)
	require.NoError(t, err)

	require.Equal(t, "claude-5-opus", resolved.Definition.ID)
	assert.Empty(t, resolved.ThinkingLevel)
	assert.NotEmpty(t, resolved.Definition.DefaultThinkingLevel,
		"the model default is still what the consumer falls back to")
}

// tagDefaultsFixture is a self-contained catalog for the multi-tag rules. It
// uses its own tags and models so the assertions describe the resolution rule
// rather than whatever the shipping catalog happens to contain.
const tagDefaultsFixture = `
tag_defaults:
  alpha:
    thinking_level: xhigh
  beta:
    thinking_level: low
models:
  - id: fixture-both-tags
    name: Fixture Both Tags
    tags: [alpha, beta]
    capabilities:
      can_reason: true
      supports_tools: true
      max_context_window: 200000
      max_output_tokens: 8192
      thinking_levels: [low, medium, high, xhigh]
    default_thinking_level: medium
    providers:
      - driver: anthropic
        api_model: fixture-both-tags

  - id: fixture-beta-only
    name: Fixture Beta Only
    tags: [beta]
    capabilities:
      can_reason: true
      supports_tools: true
      max_context_window: 200000
      max_output_tokens: 8192
      thinking_levels: [low, medium, high]
    default_thinking_level: medium
    providers:
      - driver: anthropic
        api_model: fixture-beta-only
`

// When several selector tags declare defaults and the model carries more than
// one of them, the EARLIEST selector tag wins. This is the documented rule and
// the whole reason tag defaults are deterministic — assert both orderings so a
// change to "max" or "last wins" fails here.
func TestResolve_MultiTagEarliestSelectorTagWins(t *testing.T) {
	reg, err := ParseRegistryFromBytes([]byte(tagDefaultsFixture))
	require.NoError(t, err)

	tests := []struct {
		name    string
		tags    []string
		wantID  string
		wantLvl string
	}{
		{
			name:    "alpha first wins over beta",
			tags:    []string{"alpha", "beta"},
			wantID:  "fixture-both-tags",
			wantLvl: "xhigh",
		},
		{
			name:    "beta first wins over alpha",
			tags:    []string{"beta", "alpha"},
			wantID:  "fixture-both-tags",
			wantLvl: "low",
		},
		{
			name: "a tag the resolved model does not carry contributes nothing",
			// alpha is first, but only fixture-both-tags carries it; restrict
			// to beta so the beta-only model resolves and alpha is inert.
			tags:    []string{"beta"},
			wantID:  "fixture-both-tags",
			wantLvl: "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := reg.Resolve(ModelSelector{Tags: tt.tags}, []string{"anthropic"})
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, resolved.Definition.ID)
			assert.Equal(t, tt.wantLvl, resolved.ThinkingLevel)
		})
	}
}

// A tag the resolved model does NOT carry must not contribute its default,
// even when it sits earliest in the selector. Here alpha is first but the
// alpha-carrying model has no available provider, so beta's model resolves and
// must come back at beta's level.
func TestResolve_UncarriedTagDoesNotContributeDefault(t *testing.T) {
	const fixture = tagDefaultsFixture + `
  - id: fixture-alpha-elsewhere
    name: Fixture Alpha Elsewhere
    tags: [alpha]
    capabilities:
      can_reason: true
      max_context_window: 200000
      thinking_levels: [low, medium, high, xhigh]
    providers:
      - driver: openai
        api_model: fixture-alpha-elsewhere
`
	reg, err := ParseRegistryFromBytes([]byte(fixture))
	require.NoError(t, err)

	// Only "openai" is available, so the alpha+beta model is unreachable and
	// resolution lands on the alpha-only model.
	resolved, err := reg.Resolve(ModelSelector{Tags: []string{"beta", "alpha"}}, []string{"openai"})
	require.NoError(t, err)

	require.Equal(t, "fixture-alpha-elsewhere", resolved.Definition.ID)
	assert.Equal(t, "xhigh", resolved.ThinkingLevel,
		"beta is earlier but this model does not carry it, so alpha's default applies")
}

// A non-reasoning model selected via a defaulted tag gets no thinking level at
// all — the clamp must not manufacture one.
func TestResolve_TagDefaultIsEmptyForNonReasoningModel(t *testing.T) {
	const fixture = `
tag_defaults:
  alpha:
    thinking_level: xhigh
models:
  - id: fixture-no-reasoning
    name: Fixture No Reasoning
    tags: [alpha]
    capabilities:
      can_reason: false
      max_context_window: 128000
    providers:
      - driver: anthropic
        api_model: fixture-no-reasoning
`
	reg, err := ParseRegistryFromBytes([]byte(fixture))
	require.NoError(t, err)

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{"alpha"}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Empty(t, resolved.ThinkingLevel)
}

// Parse-time validation. A tag default that names a nonexistent level, or a
// tag nothing carries, does nothing at runtime and is indistinguishable from a
// working config — so both fail the parse instead of warning.
func TestParseRegistry_RejectsInvalidTagDefaults(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown thinking level",
			yaml: `
tag_defaults:
  alpha:
    thinking_level: superhigh
models:
  - id: fixture-a
    tags: [alpha]
    capabilities: {can_reason: true, thinking_levels: [low, medium, high]}
    providers: [{driver: anthropic, api_model: fixture-a}]
`,
			wantErr: `unknown thinking level "superhigh"`,
		},
		{
			name: "tag no model carries",
			yaml: `
tag_defaults:
  nobody:
    thinking_level: high
models:
  - id: fixture-a
    tags: [alpha]
    capabilities: {can_reason: true, thinking_levels: [low, medium, high]}
    providers: [{driver: anthropic, api_model: fixture-a}]
`,
			wantErr: "no model carries this tag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRegistryFromBytes([]byte(tt.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// Clone must carry tag defaults across, or every user-configured registry
// (which is always a clone) would silently lose the feature.
func TestClone_PreservesTagDefaults(t *testing.T) {
	reg := MustGetRegistry().Clone()

	defaults, ok := reg.TagDefaultsFor(TagPowerful)
	require.True(t, ok)
	assert.Equal(t, "xhigh", defaults.ThinkingLevel)

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, allTestProviders)
	require.NoError(t, err)
	assert.Equal(t, "xhigh", resolved.ThinkingLevel)
}

// ClampThinkingLevel walks DOWN to the nearest supported level rather than
// falling back to the model's preferred default the way ReconcileThinkingLevel
// does. That distinction is the whole reason the tag default lands on `high`
// for a high-capped model instead of `medium`.
func TestClampThinkingLevel(t *testing.T) {
	reasoning := func(levels ...string) ThinkingCapability {
		return ResolveThinkingCapability(ModelCapabilities{CanReason: true, ThinkingLevels: levels})
	}

	tests := []struct {
		name  string
		cap   ThinkingCapability
		level string
		want  string
	}{
		{"supported level passes through", reasoning("low", "medium", "high", "xhigh"), "xhigh", "xhigh"},
		{"clamps down to the ceiling", reasoning("low", "medium", "high"), "xhigh", "high"},
		{"clamps across several steps", reasoning("low"), "ultra", "low"},
		{"empty requests the model default", reasoning("low", "medium", "high"), "", "medium"},
		{"non-reasoning model gets nothing", ResolveThinkingCapability(ModelCapabilities{}), "xhigh", ""},
		{"unknown level defers to reconcile", reasoning("low", "medium", "high"), "bogus", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClampThinkingLevel(tt.cap, tt.level))
		})
	}
}
