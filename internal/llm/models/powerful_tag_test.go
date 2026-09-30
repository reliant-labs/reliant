// Copyright (c) 2025 Reliant Labs
package models

import (
	"strings"
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
	"antigravity",
}

// `powerful` is the frontier tier that sits above flagship. List position is
// resolution priority, so this pins which model a bare [powerful] selector
// picks — inserting a model earlier in the tag's list would otherwise repoint
// it silently.
func TestResolve_PowerfulTagTargetIsPinned(t *testing.T) {
	reg := MustGetRegistry()

	resolved, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, allTestProviders)
	require.NoError(t, err)
	assert.Equal(t, "claude-5.1-fable", resolved.Definition.ID)
}

// Every powerful entry runs its model at a top-tier effort — that is what
// "powerful models think harder" means. An entry that ran at medium would
// quietly undercut the whole point of the tier.
func TestPowerfulEntriesRunAtTopTierEffort(t *testing.T) {
	reg := MustGetRegistry()

	entries := reg.TagEntries(TagPowerful)
	require.NotEmpty(t, entries, "expected at least one powerful entry")

	for _, entry := range entries {
		model, ok := reg.GetDefinition(entry.Model)
		require.True(t, ok)
		levels := model.Capabilities.ThinkingLevels
		require.NotEmpty(t, levels, "%s: powerful model must declare thinking levels", model.ID)

		// `max`/`ultra` exist but are opt-in, so the bar is xhigh where the
		// model supports it and the top declared level otherwise.
		want := "xhigh"
		if !contains(levels, want) {
			want = levels[len(levels)-1]
		}
		assert.Equal(t, want, entry.ThinkingLevel,
			"%s: powerful entry should run at its model's top practical thinking level", model.ID)
	}
}

// The tag must list exactly the models we intended, in resolution order.
func TestPowerfulTagMembership(t *testing.T) {
	reg := MustGetRegistry()

	var ids []string
	for _, model := range reg.GetModelsByTag(TagPowerful) {
		ids = append(ids, model.ID)
	}

	// claude-5.5-opus is deliberately ABSENT: it is the flagship pick, and
	// listing it here would collapse the two tiers onto one model.
	// gpt-6.1-sol follows astra, so an openai-only user's [powerful] stays
	// on astra; it is reached only when astra is not servable.
	assert.Equal(t, []string{
		"claude-5.1-fable",
		"gpt-6-astra",
		"gpt-6.1-sol",
		"gpt-5.6-sol",
		"gemini-3.8-flash",
		"vertex-claude-5.1-fable",
	}, ids)
}

// Opus 5.5 must be reachable on all four providers the product exposes, with
// the bare api_model everywhere except openrouter, which namespaces it. Note
// the api_model is claude-opus-5-5, NOT claude-opus-5.5 — the capture's wire
// spelling uses dashes, and the dotted form is the catalog id only.
func TestClaude55OpusProviderMappings(t *testing.T) {
	reg := MustGetRegistry()

	def, ok := reg.GetDefinition("claude-5.5-opus")
	require.True(t, ok)

	got := make(map[string]string, len(def.Providers))
	for _, p := range def.Providers {
		got[p.Driver] = p.APIModel
	}

	assert.Equal(t, map[string]string{
		"anthropic":  "claude-opus-5-5",
		"openrouter": "anthropic/claude-opus-5-5",
		"reliant":    "claude-opus-5-5",
		"vertexai":   "claude-opus-5-5",
	}, got)

	assert.Equal(t, "adaptive", def.DriverSettings.ThinkingMode)

	// The 2.1.280 capture sends max_tokens 128000 — double every other Claude
	// entry. A copy-pasted 64000 would silently halve the output ceiling.
	assert.Equal(t, 128000, def.Capabilities.MaxOutputTokens)
}

// Opus 5.5 is the flagship pick on EVERY provider that serves it, not just on
// whichever one happens to sort first. A user with only Vertex configured must
// get the same answer from [flagship] as a user with only Anthropic — the two
// catalog entries (claude-5.5-opus and vertex-claude-5.5-opus) exist precisely
// so that holds, and each must lead its own section of the file.
func TestFlagshipResolvesToOpus55OnEveryProvider(t *testing.T) {
	reg := MustGetRegistry()

	for _, provider := range []string{"anthropic", "openrouter", "reliant", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			resolved, err := reg.Resolve(
				ModelSelector{Tags: []string{TagFlagship}},
				[]string{provider},
			)
			require.NoError(t, err)

			// claude-5.5-opus carries its OWN vertexai mapping and leads the
			// flagship list, so it wins for every provider — the dedicated
			// vertex-claude-5.5-opus entry is never what a bare [flagship]
			// selector reaches. That entry exists for explicit id selection and
			// for parity with the other vertex-* duplicates; the assertion that
			// matters is that both spellings reach the same wire model.
			assert.Equal(t, "claude-5.5-opus", resolved.Definition.ID)
			assert.Equal(t, provider, resolved.Provider.Driver)
			assert.Equal(t, "claude-opus-5-5", resolvedAPIModelSuffix(resolved.Provider.APIModel))
		})
	}

	// The dedicated Vertex entry is reachable by explicit id and lands on the
	// same wire model, so the two spellings can never diverge.
	byID, err := reg.Resolve(ModelSelector{ID: "vertex-claude-5.5-opus"}, []string{"vertexai"})
	require.NoError(t, err)
	assert.Equal(t, "vertexai", byID.Provider.Driver)
	assert.Equal(t, "claude-opus-5-5", byID.Provider.APIModel)
}

// resolvedAPIModelSuffix strips openrouter's `anthropic/` namespace so one
// assertion covers every provider's spelling of the same wire model.
func resolvedAPIModelSuffix(apiModel string) string {
	if idx := strings.LastIndex(apiModel, "/"); idx != -1 {
		return apiModel[idx+1:]
	}
	return apiModel
}

// Flagship and powerful must stay DIFFERENT models. Listing 5.5 under
// `powerful` ahead of fable would collapse the two tiers onto one model
// without any test failing on the tag list alone.
func TestFlagshipAndPowerfulResolveToDifferentModels(t *testing.T) {
	reg := MustGetRegistry()

	flagship, err := reg.Resolve(ModelSelector{Tags: []string{TagFlagship}}, allTestProviders)
	require.NoError(t, err)
	powerful, err := reg.Resolve(ModelSelector{Tags: []string{TagPowerful}}, allTestProviders)
	require.NoError(t, err)

	assert.Equal(t, "claude-5.5-opus", flagship.Definition.ID)
	assert.Equal(t, "claude-5.1-fable", powerful.Definition.ID)

	for _, id := range []string{"claude-5.5-opus", "vertex-claude-5.5-opus"} {
		tags := reg.TagsOf(id)
		assert.Contains(t, tags, TagFlagship, "%s must be flagship", id)
		assert.NotContains(t, tags, TagPowerful,
			"%s must not be powerful — it would steal the powerful tier too", id)
	}
}

// Adding models reorders nothing unless we say so. Pin the global winners so
// an edit to a tag list has to change them on purpose.
func TestResolve_ExistingTagTargetsUnchangedByNewModels(t *testing.T) {
	reg := MustGetRegistry()

	// TagFast was gpt-5.3-codex-spark, which sat first in the list. That
	// model was removed: codex was its only provider and the ChatGPT-account
	// backend refuses it, so it was unreachable by construction. The next fast
	// entry is gemini-3.5-flash. This is a per-user resolution in practice —
	// it filters to the providers a user has configured — so this global pin
	// is a canary for accidental reordering, not the model most users get.
	for tag, want := range map[string]string{
		// claude-5.5-opus leads flagship. This moved from claude-5-opus
		// deliberately when 5.5 shipped.
		TagFlagship: "claude-5.5-opus",
		// claude-5-opus leads moderate: implementation speed over 5.5
		// (it measured ~3x faster per edit at the same effort).
		TagModerate: "claude-5-opus",
		TagCheap:    "claude-4.5-haiku",
		TagFast:     "gemini-3.5-flash",
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

	// tags is the sorted derived set (TagsOf); effort is what the model runs at
	// under the tier it was added for (firstTag).
	tests := []struct {
		id            string
		tags          []string
		levels        []string
		firstTag      string
		effort        string
		contextWindow int
		outputTokens  int
	}{
		{"claude-5.1-fable", []string{TagFlagship, TagPowerful, TagReasoning},
			[]string{"low", "medium", "high", "xhigh"}, TagPowerful, "xhigh", 1000000, 64000},
		{"vertex-claude-5.1-fable", []string{TagFlagship, TagPowerful, TagReasoning},
			[]string{"low", "medium", "high", "xhigh"}, TagPowerful, "xhigh", 1000000, 64000},
		{"gemini-3.8-flash", []string{TagFlagship, TagPowerful, TagReasoning},
			[]string{"low", "medium", "high"}, TagPowerful, "high", 1048576, 65536},
		{"gemini-3.7-flash", []string{TagFlagship, TagModerate, TagReasoning},
			[]string{"low", "medium", "high"}, TagFlagship, "medium", 1048576, 65536},
		{"gemini-3.6-flash", []string{TagModerate, TagReasoning},
			[]string{"low", "medium", "high"}, TagModerate, "medium", 1048576, 65536},
		{"gemini-3.5-flash", []string{TagFast, TagModerate, TagReasoning},
			[]string{"low", "medium", "high"}, TagFast, "low", 1048576, 65536},
		{"gpt-6-astra", []string{TagFlagship, TagPowerful, TagReasoning},
			[]string{"low", "medium", "high", "xhigh", "max"}, TagPowerful, "xhigh", 1050000, 128000},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			def, ok := reg.GetDefinition(tt.id)
			require.True(t, ok, "expected %s in the registry", tt.id)

			assert.Equal(t, tt.tags, reg.TagsOf(tt.id))
			assert.True(t, def.Capabilities.CanReason)
			assert.True(t, def.Capabilities.SupportsTools)
			assert.Equal(t, tt.levels, def.Capabilities.ThinkingLevels)
			assert.Equal(t, tt.effort, entryLevel(t, reg, tt.firstTag, tt.id))
			assert.Equal(t, tt.contextWindow, def.Capabilities.MaxContextWindow)
			assert.Equal(t, tt.outputTokens, def.Capabilities.MaxOutputTokens)
			require.NotEmpty(t, def.Providers)
		})
	}
}

// Astra tops out at `max`. The codex capture's own spawn_agent description
// claims `ultra` for astra, but that string is written by the client, while
// OpenAI's API reference lists low/medium/high/xhigh/max. Declaring ultra here
// would send an effort the model does not accept, so the absence is pinned
// rather than left to a reading of the table above.
func TestAstraStopsAtMaxEffort(t *testing.T) {
	reg := MustGetRegistry()

	def, ok := reg.GetDefinition("gpt-6-astra")
	require.True(t, ok)

	assert.Contains(t, def.Capabilities.ThinkingLevels, "max")
	assert.NotContains(t, def.Capabilities.ThinkingLevels, "ultra")
}

// Astra is served by the three providers that can actually reach it. Vertex AI
// and the managed Reliant gateway are deliberately absent: Vertex's Model
// Garden offers only OpenAI's open-weight gpt-oss models, and our gateway
// routes every model through vertex_ai, so either mapping would put a picker
// option in front of users that fails at request time.
func TestAstraProviderMappings(t *testing.T) {
	reg := MustGetRegistry()

	def, ok := reg.GetDefinition("gpt-6-astra")
	require.True(t, ok)

	got := make(map[string]string, len(def.Providers))
	for _, p := range def.Providers {
		got[p.Driver] = p.APIModel
	}

	assert.Equal(t, map[string]string{
		"codex":      "gpt-6-astra",
		"openai":     "gpt-6-astra",
		"openrouter": "openai/gpt-6-astra",
	}, got)
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
// Terra carries `moderate`, NOT `fast`. `fast` resolves globally by definition
// order, so tagging terra would repoint @fast away from gemini-3.5-flash for
// every user, and terra bills at gpt-5.5's rate, which is not a fast-tier
// price. The codex driver has no fast model by design; titling and compaction
// pass a [fast, moderate] preference ladder and degrade to gpt-5.5 instead.
func TestGPT56FamilyTagLadder(t *testing.T) {
	reg := MustGetRegistry()

	for id, wantTags := range map[string][]string{
		"gpt-5.6-sol":   {TagPowerful, TagReasoning},
		"gpt-5.6-luna":  {TagFlagship, TagReasoning},
		"gpt-5.6-terra": {TagModerate, TagReasoning},
	} {
		_, ok := reg.GetDefinition(id)
		require.True(t, ok, "expected %s in the registry", id)
		assert.Equal(t, wantTags, reg.TagsOf(id), "%s tags", id)
	}

	assert.Equal(t, "xhigh", entryLevel(t, reg, TagPowerful, "gpt-5.6-sol"))
}

// entryLevel is the thinking_level a tag's entry declares for a model,
// failing the test when the tag does not list the model.
func entryLevel(t *testing.T, reg *ModelRegistry, tag, modelID string) string {
	t.Helper()
	for _, entry := range reg.TagEntries(tag) {
		if entry.Model == modelID {
			return entry.ThinkingLevel
		}
	}
	t.Fatalf("tag %q does not list %s", tag, modelID)
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
