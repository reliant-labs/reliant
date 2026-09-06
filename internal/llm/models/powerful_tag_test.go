// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allTestProviders is every driver the embedded catalog references, so a
// resolution here is constrained by definition order and tags alone rather
// than by which credentials happen to exist.
var allTestProviders = []string{
	"anthropic", "openai", "openrouter", "reliant",
	"gemini", "vertexai", "codex", "copilot", "xai", "ollama",
}

// `powerful` is the frontier tier that sits above flagship. Definition order is
// resolution priority, so this pins which model a bare [powerful] selector
// picks — inserting a powerful model earlier in the YAML would otherwise
// repoint it silently.
func TestResolve_PowerfulTagTargetIsPinned(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, allTestProviders)
	require.NoError(t, err)
	assert.Equal(t, "claude-5.1-fable", resolved.Definition.ID)
}

// Every powerful model carries a top-tier default_thinking_level in its own
// definition — that is how "powerful models think harder" is expressed, since
// the tag itself carries no thinking defaults. A powerful model that defaulted
// to medium would quietly undercut the whole point of the tier.
func TestPowerfulModelsCarryTopTierThinkingDefaults(t *testing.T) {
	reg := MustGetRegistry()

	powerful := reg.GetModelsByTag(TagPowerful)
	require.NotEmpty(t, powerful, "expected at least one powerful-tagged model")

	for _, model := range powerful {
		levels := model.Capabilities.ThinkingLevels
		require.NotEmpty(t, levels, "%s: powerful model must declare thinking levels", model.ID)

		// `max`/`ultra` exist but are opt-in, so the bar is xhigh where the
		// model supports it and the top declared level otherwise.
		want := "xhigh"
		if !contains(levels, want) {
			want = levels[len(levels)-1]
		}
		assert.Equal(t, want, model.DefaultThinkingLevel,
			"%s: powerful model should default to its top practical thinking level", model.ID)
	}
}

// The tag must be indexed under exactly the models we intended, in definition
// order.
func TestPowerfulTagMembership(t *testing.T) {
	reg := MustGetRegistry()

	var ids []string
	for _, model := range reg.GetModelsByTag(TagPowerful) {
		ids = append(ids, model.ID)
	}

	assert.Equal(t, []string{
		"claude-5.1-fable",
		"gpt-5.6-sol",
		"gemini-3.8-flash",
		"vertex-claude-5.1-fable",
	}, ids)
}

// Adding models reorders nothing unless we say so: [flagship] and [fast] are
// resolved by definition order, and several of the new entries carry those
// tags. Pin the global winners so an insertion has to change them on purpose.
func TestResolve_ExistingTagTargetsUnchangedByNewModels(t *testing.T) {
	reg := MustGetRegistry()

	for tag, want := range map[string]string{
		TagFlagship: "claude-5-opus",
		TagModerate: "claude-5-sonnet",
		TagCheap:    "claude-4.5-haiku",
		TagFast:     "gpt-5.3-codex-spark",
	} {
		resolved, err := reg.Resolve(ModelSelector{Tags: []string{tag}}, allTestProviders)
		require.NoError(t, err, "resolving tag %q", tag)
		assert.Equal(t, want, resolved.Definition.ID, "tag %q resolved unexpectedly", tag)
	}
}

// The new Claude and Gemini entries have to parse with the capabilities the
// drivers rely on — a can_reason:false or an empty thinking_levels would
// silently disable extended thinking rather than fail loudly.
func TestNewModelDefinitionsParseWithExpectedCapabilities(t *testing.T) {
	reg := MustGetRegistry()

	tests := []struct {
		id            string
		tags          []string
		levels        []string
		defaultLevel  string
		contextWindow int
		outputTokens  int
	}{
		{"claude-5.1-fable", []string{TagPowerful, TagFlagship, TagReasoning},
			[]string{"low", "medium", "high", "xhigh"}, "xhigh", 1000000, 64000},
		{"vertex-claude-5.1-fable", []string{TagPowerful, TagFlagship, TagReasoning},
			[]string{"low", "medium", "high", "xhigh"}, "xhigh", 1000000, 64000},
		{"gemini-3.8-flash", []string{TagPowerful, TagFlagship, TagReasoning},
			[]string{"low", "medium", "high"}, "high", 1048576, 65536},
		{"gemini-3.7-flash", []string{TagFlagship, TagModerate, TagReasoning},
			[]string{"low", "medium", "high"}, "medium", 1048576, 65536},
		{"gemini-3.6-flash", []string{TagModerate, TagReasoning},
			[]string{"low", "medium", "high"}, "medium", 1048576, 65536},
		{"gemini-3.5-flash", []string{TagModerate, TagFast, TagReasoning},
			[]string{"low", "medium", "high"}, "low", 1048576, 65536},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			def, ok := reg.GetDefinition(tt.id)
			require.True(t, ok, "expected %s in the registry", tt.id)

			assert.Equal(t, tt.tags, def.Tags)
			assert.True(t, def.Capabilities.CanReason)
			assert.True(t, def.Capabilities.SupportsTools)
			assert.Equal(t, tt.levels, def.Capabilities.ThinkingLevels)
			assert.Equal(t, tt.defaultLevel, def.DefaultThinkingLevel)
			assert.Equal(t, tt.contextWindow, def.Capabilities.MaxContextWindow)
			assert.Equal(t, tt.outputTokens, def.Capabilities.MaxOutputTokens)
			require.NotEmpty(t, def.Providers)
		})
	}
}

// Fable 5.1 must be reachable on all four providers the product exposes, with
// the bare api_model everywhere except openrouter, which namespaces it.
func TestClaude51FableProviderMappings(t *testing.T) {
	reg := MustGetRegistry()

	def, ok := reg.GetDefinition("claude-5.1-fable")
	require.True(t, ok)

	got := make(map[string]string, len(def.Providers))
	for _, p := range def.Providers {
		got[p.Driver] = p.APIModel
	}

	assert.Equal(t, map[string]string{
		"anthropic":  "claude-fable-5-1",
		"openrouter": "anthropic/claude-fable-5-1",
		"reliant":    "claude-fable-5-1",
		"vertexai":   "claude-fable-5-1",
	}, got)

	// Adaptive thinking is what makes the driver emit `thinking:{type:adaptive}`
	// rather than a token budget.
	assert.Equal(t, "adaptive", def.DriverSettings.ThinkingMode)
}

// The GPT-5.6 family was re-laddered: sol is the frontier pick, luna the
// family flagship, terra the faster/cheaper one. Pin it — these three are
// distinguished only by tags, so a mistaken edit is invisible at runtime.
//
// Terra carries `moderate`, NOT `fast`: TagFast routes @fast, chat titling and
// compaction for the whole codex driver and stays on gpt-5.3-codex-spark.
func TestGPT56FamilyTagLadder(t *testing.T) {
	reg := MustGetRegistry()

	for id, wantTags := range map[string][]string{
		"gpt-5.6-sol":   {TagPowerful, TagReasoning},
		"gpt-5.6-luna":  {TagFlagship, TagReasoning},
		"gpt-5.6-terra": {TagModerate, TagReasoning},
	} {
		def, ok := reg.GetDefinition(id)
		require.True(t, ok, "expected %s in the registry", id)
		assert.Equal(t, wantTags, def.Tags, "%s tags", id)
	}

	sol, _ := reg.GetDefinition("gpt-5.6-sol")
	assert.Equal(t, "xhigh", sol.DefaultThinkingLevel)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
