// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tag is an ordered list of {model, thinking_level}: selecting it picks a
// model AND the effort that model runs at. These tests pin the resolution
// rule, the precedence, and the clamp, since all three are invisible at
// runtime when wrong: the request still goes out, just at the wrong effort.

// Agent presets name a TIER, never an effort: `general` asks for [flagship],
// `implementer` asks for [moderate]. The two tiers are deliberately DIFFERENT
// models: flagship is claude-5.5-opus @ xhigh for orchestration, moderate is
// claude-5-opus @ medium for implementation, where 5-opus measured ~3x faster
// per edit than 5.5 at the same effort. These are the shipping pins —
// changing either is a cost/speed/quality decision, so it must fail here.
func TestResolve_AgentTiersPinModelAndEffort(t *testing.T) {
	reg := MustGetRegistry()

	for _, tt := range []struct {
		tag       string
		wantModel string
		vertex    string // the vertex-only twin, which must mirror the entry
		wantLevel string
	}{
		{TagFlagship, "claude-5.5-opus", "vertex-claude-5.5-opus", "xhigh"},
		{TagModerate, "claude-5-opus", "vertex-claude-5-opus", "medium"},
	} {
		t.Run(tt.tag, func(t *testing.T) {
			for _, id := range []string{tt.wantModel, tt.vertex} {
				assert.Equal(t, tt.wantLevel, entryLevel(t, reg, tt.tag, id),
					"%s must run at %s under %q", id, tt.wantLevel, tt.tag)
			}

			for _, providers := range [][]string{allTestProviders, {"anthropic"}, {"reliant"}} {
				resolved, err := reg.Resolve(ModelSelector{Tags: []string{tt.tag}}, providers)
				require.NoError(t, err)
				assert.Equal(t, tt.wantModel, resolved.Definition.ID, "providers %v", providers)
				assert.Equal(t, tt.wantLevel, resolved.ThinkingLevel, "providers %v", providers)
			}

			// A Vertex-only user reaches the same wire model at the same effort —
			// through the twin when the primary entry has no vertexai mapping
			// (claude-5-opus), or directly when it does (claude-5.5-opus).
			vertex, err := reg.Resolve(ModelSelector{Tags: []string{tt.tag}}, []string{"vertexai"})
			require.NoError(t, err)
			assert.Contains(t, []string{tt.wantModel, tt.vertex}, vertex.Definition.ID)
			assert.Equal(t, "vertexai", vertex.Provider.Driver)
			assert.Equal(t, tt.wantLevel, vertex.ThinkingLevel)
		})
	}
}

// The embedded catalog's `powerful` tier runs its models at xhigh, and must
// come back carrying it without the caller ever naming a level.
func TestResolve_PowerfulTagCarriesItsEffort(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, allTestProviders)
	require.NoError(t, err)

	assert.Equal(t, "claude-5.1-fable", resolved.Definition.ID)
	assert.Equal(t, "xhigh", resolved.ThinkingLevel)
}

// gemini-3.8-flash is listed under `powerful` and tops out at high, so its
// entry must never ask for more than the model can do — and resolution must
// land on high, not fall back to the model's preferred medium.
func TestResolve_PowerfulEffortRespectsModelCeiling(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, []string{"gemini"})
	require.NoError(t, err)

	require.Equal(t, "gemini-3.8-flash", resolved.Definition.ID)
	require.NotContains(t, resolved.Definition.Capabilities.ThinkingLevels, "xhigh",
		"this case only exercises the ceiling while the model tops out below xhigh")
	assert.Equal(t, "high", resolved.ThinkingLevel)
}

// Selecting by explicit id picks up NO tier effort, even for a model listed
// under an effortful tier: the effort describes how the model was chosen. It
// gets the model's capability default instead — medium where supported.
func TestResolve_ByIDUsesCapabilityDefault(t *testing.T) {
	reg := MustGetRegistry()

	for _, id := range []string{"claude-5.1-fable", "claude-5.5-opus"} {
		resolved, err := reg.Resolve(ModelSelector{ID: id}, allTestProviders)
		require.NoError(t, err)

		require.NotEmpty(t, reg.TagsOf(id))
		assert.Equal(t, "medium", resolved.ThinkingLevel,
			"%s by id must not inherit a tier's effort", id)
	}
}

// entriesFixture is a self-contained catalog for the resolution rules. It uses
// its own tags and models so the assertions describe the rule rather than
// whatever the shipping catalog happens to contain.
const entriesFixture = `
tags:
  alpha:
    - {model: fixture-both-tags, thinking_level: xhigh}
  beta:
    - {model: fixture-both-tags, thinking_level: low}
    - {model: fixture-beta-only}
  gamma:
    - {model: fixture-capped, thinking_level: xhigh}
models:
  - id: fixture-both-tags
    name: Fixture Both Tags
    capabilities:
      can_reason: true
      max_context_window: 200000
      thinking_levels: [low, medium, high, xhigh]
    providers:
      - driver: anthropic
        api_model: fixture-both-tags

  - id: fixture-beta-only
    name: Fixture Beta Only
    capabilities:
      can_reason: true
      max_context_window: 200000
      thinking_levels: [low, medium, high]
    providers:
      - driver: openai
        api_model: fixture-beta-only

  - id: fixture-capped
    name: Fixture Capped
    capabilities:
      can_reason: true
      max_context_window: 200000
      thinking_levels: [low, medium, high]
    providers:
      - driver: anthropic
        api_model: fixture-capped

  - id: fixture-no-reasoning
    name: Fixture No Reasoning
    capabilities:
      can_reason: false
      max_context_window: 128000
    providers:
      - driver: anthropic
        api_model: fixture-no-reasoning
`

func TestResolve_TagEntryRules(t *testing.T) {
	reg, err := ParseRegistryFromBytes([]byte(entriesFixture))
	require.NoError(t, err)

	tests := []struct {
		name      string
		tags      []string
		providers []string
		wantID    string
		wantLevel string
	}{
		// The same model under two tags runs at each tag's own effort.
		{"alpha entry's effort", []string{"alpha"}, []string{"anthropic"}, "fixture-both-tags", "xhigh"},
		{"beta entry's effort for the same model", []string{"beta"}, []string{"anthropic"}, "fixture-both-tags", "low"},
		// When several selector tags list the resolved model, the EARLIEST
		// selector tag's entry wins — both orderings, so a change to "max" or
		// "last wins" fails here.
		{"alpha first wins over beta", []string{"alpha", "beta"}, []string{"anthropic"}, "fixture-both-tags", "xhigh"},
		{"beta first wins over alpha", []string{"beta", "alpha"}, []string{"anthropic"}, "fixture-both-tags", "low"},
		// First entry with an available provider wins; an entry with no level
		// runs at the model's capability default.
		{"falls through to the next available entry", []string{"beta"}, []string{"openai"}, "fixture-beta-only", "medium"},
		// A tag that does not list the resolved model contributes nothing:
		// alpha is first, but only beta lists the openai model.
		{"an unlisting tag contributes no effort", []string{"alpha", "beta"}, []string{"openai"}, "fixture-beta-only", "medium"},
		// An aspirational level clamps DOWN to the model's ceiling.
		{"effort clamps to the model's ceiling", []string{"gamma"}, []string{"anthropic"}, "fixture-capped", "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := reg.Resolve(ModelSelector{Tags: tt.tags}, tt.providers)
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, resolved.Definition.ID)
			assert.Equal(t, tt.wantLevel, resolved.ThinkingLevel)
		})
	}
}

// A non-reasoning model gets no thinking level at all, whatever its entry
// asks for — the clamp must not manufacture one.
func TestResolve_NoEffortForNonReasoningModel(t *testing.T) {
	reg, err := ParseRegistryFromBytes([]byte(entriesFixture + `
  - id: fixture-late
    capabilities: {can_reason: false}
    providers: [{driver: xai, api_model: late}]
`))
	require.NoError(t, err)
	require.NoError(t, reg.applyUserTags(map[string][]TagEntry{
		"delta": {{Model: "fixture-no-reasoning", ThinkingLevel: "xhigh"}},
	}))

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{"delta"}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Empty(t, resolved.ThinkingLevel)

	byID, err := reg.Resolve(ModelSelector{ID: "fixture-no-reasoning"}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Empty(t, byID.ThinkingLevel)
}

// A model's tags are derived from the lists that name it, sorted, so every
// surface that reports tags (ListModels, the picker) agrees with resolution.
func TestTagsOf_IsDerivedFromTagLists(t *testing.T) {
	reg, err := ParseRegistryFromBytes([]byte(entriesFixture))
	require.NoError(t, err)

	assert.Equal(t, []string{"alpha", "beta"}, reg.TagsOf("fixture-both-tags"))
	assert.Equal(t, []string{"beta"}, reg.TagsOf("fixture-beta-only"))
	assert.Empty(t, reg.TagsOf("fixture-no-reasoning"))
}

// User tag entries go FIRST; the built-in list follows as the fallback. A
// model the user lists takes the user's position and effort — including
// re-tuning the effort of a model already in the tier.
func TestMergeUserConfig_UserTagEntriesLeadAndRetune(t *testing.T) {
	reg := MustGetRegistry().Clone()

	require.NoError(t, reg.MergeUserConfig(&UserModelsConfig{
		Tags: map[string][]TagEntry{
			TagModerate: {{Model: "claude-5-opus", ThinkingLevel: "low"}},
			TagFlagship: {{Model: "gpt-5.5", ThinkingLevel: "high"}},
		},
	}))

	moderate, err := reg.Resolve(ModelSelector{Tags: []string{TagModerate}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "claude-5-opus", moderate.Definition.ID)
	assert.Equal(t, "low", moderate.ThinkingLevel, "the user's effort replaces the built-in one")

	flagship, err := reg.Resolve(ModelSelector{Tags: []string{TagFlagship}}, []string{"anthropic", "openai"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.5", flagship.Definition.ID)
	assert.Equal(t, "high", flagship.ThinkingLevel)

	// The built-in list still backs the user's pick: without openai, flagship
	// falls through to the built-in leader at its built-in effort.
	fallback, err := reg.Resolve(ModelSelector{Tags: []string{TagFlagship}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "claude-5.5-opus", fallback.Definition.ID)
	assert.Equal(t, "xhigh", fallback.ThinkingLevel)

	// The model appears exactly once in the merged list.
	count := 0
	for _, entry := range reg.TagEntries(TagModerate) {
		if entry.Model == "claude-5-opus" {
			count++
		}
	}
	assert.Equal(t, 1, count)

	// The shared registry is untouched.
	original, err := MustGetRegistry().Resolve(ModelSelector{Tags: []string{TagModerate}}, []string{"anthropic"})
	require.NoError(t, err)
	assert.Equal(t, "medium", original.ThinkingLevel)
}

// A custom model joins a tier only through a user tag entry — models carry
// no tags of their own.
func TestMergeUserConfig_CustomModelJoinsTierViaUserTags(t *testing.T) {
	reg := MustGetRegistry().Clone()

	require.NoError(t, reg.MergeUserConfig(&UserModelsConfig{
		Custom: []ModelDefinition{{
			ID:           "local-qwen",
			Capabilities: ModelCapabilities{CanReason: true, ThinkingLevels: []string{"low", "medium", "high"}},
			Providers:    []ProviderMapping{{Driver: "local", APIModel: "qwen2.5:32b"}},
		}},
		Tags: map[string][]TagEntry{TagFast: {{Model: "local-qwen", ThinkingLevel: "low"}}},
	}))

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagFast}}, []string{"local"})
	require.NoError(t, err)
	assert.Equal(t, "local-qwen", resolved.Definition.ID)
	assert.Equal(t, "low", resolved.ThinkingLevel)
	assert.Equal(t, []string{TagFast}, reg.TagsOf("local-qwen"))
}

// Parse-time validation. An entry naming a model nothing defines, a level
// nothing understands, or a model listed twice (the second position can never
// win) does nothing at runtime and is indistinguishable from a working config
// — so each fails the parse instead of warning. User config goes through the
// same gate.
func TestParseRegistry_RejectsInvalidTagEntries(t *testing.T) {
	const model = `
models:
  - id: fixture-a
    capabilities: {can_reason: true, thinking_levels: [low, medium, high]}
    providers: [{driver: anthropic, api_model: fixture-a}]
`
	tests := []struct {
		name    string
		tags    string
		wantErr string
	}{
		{"unknown model", "tags:\n  alpha:\n    - {model: nobody}\n", `unknown model "nobody"`},
		{"unknown thinking level", "tags:\n  alpha:\n    - {model: fixture-a, thinking_level: superhigh}\n", `unknown thinking level "superhigh"`},
		{"model listed twice", "tags:\n  alpha:\n    - {model: fixture-a}\n    - {model: fixture-a}\n", "listed more than once"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRegistryFromBytes([]byte(tt.tags + model))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	t.Run("user config", func(t *testing.T) {
		err := MustGetRegistry().Clone().MergeUserConfig(&UserModelsConfig{
			Tags: map[string][]TagEntry{TagFlagship: {{Model: "nobody"}}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown model "nobody"`)
	})
}

// No shipping entry may ask a non-reasoning model for an effort: it would be
// silently dropped, and the entry would look like it meant something.
func TestShippingTagEntries_EffortOnlyOnReasoningModels(t *testing.T) {
	reg := MustGetRegistry()

	for _, tag := range reg.ListAllTags() {
		for _, entry := range reg.TagEntries(tag) {
			model, ok := reg.GetDefinition(entry.Model)
			require.True(t, ok)
			if entry.ThinkingLevel != "" {
				assert.True(t, model.Capabilities.CanReason,
					"tags[%s]: %s cannot reason but declares thinking_level %q", tag, entry.Model, entry.ThinkingLevel)
				assert.True(t, SupportsThinkingLevelForCaps(model.Capabilities, entry.ThinkingLevel),
					"tags[%s]: %s does not support %q (supports %v)", tag, entry.Model, entry.ThinkingLevel,
					SupportedThinkingLevels(model.Capabilities))
			}
		}
	}
}

// Clone must carry tag entries across, and be independent of the original,
// or every user-configured registry (which is always a clone) would either
// lose the feature or leak into the shared one.
func TestClone_CarriesTagEntriesIndependently(t *testing.T) {
	original := MustGetRegistry()
	cloned := original.Clone()

	assert.Equal(t, original.TagEntries(TagPowerful), cloned.TagEntries(TagPowerful))

	require.NoError(t, cloned.applyUserTags(map[string][]TagEntry{TagPowerful: {{Model: "gpt-5.5"}}}))
	assert.Equal(t, "gpt-5.5", cloned.TagEntries(TagPowerful)[0].Model)
	assert.Equal(t, "claude-5.1-fable", original.TagEntries(TagPowerful)[0].Model)
}

// ClampThinkingLevel walks DOWN to the nearest supported level rather than
// falling back to the model's preferred default the way ReconcileThinkingLevel
// does. That distinction is the whole reason an xhigh entry lands on `high`
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
