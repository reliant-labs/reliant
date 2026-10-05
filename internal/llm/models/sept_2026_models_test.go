// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Sonnet 5.5 (2026-09-28) and the GPT-6 Astra/Sol/Terra/Luna models.
// Every figure below is taken from the vendor's own model page —
// platform.claude.com/docs/en/models/sonnet-5-5 and
// developers.openai.com/api/docs/models/{gpt-6-astra,gpt-6-sol,gpt-6-terra,gpt-6-luna}
// — not from a neighbouring catalog entry, because the entries they sit next to
// differ from them in exactly the fields that are easy to copy by mistake.

// providerAPIModels maps each provider driver to the api_model it sends.
func providerAPIModels(t *testing.T, reg *ModelRegistry, id string) map[string]string {
	t.Helper()
	def, ok := reg.GetDefinition(id)
	require.True(t, ok, "expected %s in the registry", id)

	got := make(map[string]string, len(def.Providers))
	for _, p := range def.Providers {
		got[p.Driver] = p.APIModel
	}
	return got
}

// The wire spelling is claude-sonnet-5-5 with dashes; the catalog id is the
// dotted claude-5.5-sonnet. Mixing the two is the "Invalid model name" failure
// pkg/llmcatalog exists to prevent.
//
// There is no vertexai mapping on the primary entry, deliberately. Vertex
// users reach it through vertex-claude-5.5-sonnet, the same split
// claude-5-sonnet uses.
//
// No copilot mapping either: Copilot's /models reports sonnet-5.5 for no
// account we have probed, and a mapping it does not serve 400s upstream.
func TestClaude55SonnetProviderMappings(t *testing.T) {
	reg := MustGetRegistry()

	assert.Equal(t, map[string]string{
		"anthropic":  "claude-sonnet-5-5",
		"openrouter": "anthropic/claude-sonnet-5.5",
		"reliant":    "claude-sonnet-5-5",
		"copilot":    "claude-sonnet-5.5",
	}, providerAPIModels(t, reg, "claude-5.5-sonnet"))

	assert.Equal(t, map[string]string{
		"vertexai": "claude-sonnet-5-5",
	}, providerAPIModels(t, reg, "vertex-claude-5.5-sonnet"))
}

// Sonnet 5.5 doubles Sonnet 5's output ceiling to 128K. Both entries must
// agree: they are two spellings of one wire model.
func TestClaude55SonnetCapabilities(t *testing.T) {
	reg := MustGetRegistry()

	for _, id := range []string{"claude-5.5-sonnet", "vertex-claude-5.5-sonnet"} {
		t.Run(id, func(t *testing.T) {
			def, ok := reg.GetDefinition(id)
			require.True(t, ok)

			assert.Equal(t, 1000000, def.Capabilities.MaxContextWindow)
			assert.Equal(t, 128000, def.Capabilities.MaxOutputTokens,
				"a copy-pasted 64000 from claude-5-sonnet would halve the output ceiling")
			assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, def.Capabilities.ThinkingLevels)
			assert.True(t, def.Capabilities.CanReason)
			assert.True(t, def.Capabilities.SupportsTools)
			assert.Equal(t, "adaptive", def.DriverSettings.ThinkingMode,
				"sonnet-5.5 rejects budget_tokens; only adaptive thinking is accepted")
		})
	}
}

// GPT-6 Sol, Terra and Luna reach users through openai and openrouter only.
//
// codex is absent on purpose. The ChatGPT-account backend serves a subset of
// the catalog that has to be probed per model (see the note above gpt-5.5 in
// models.yaml), and the GPT-6 family's request envelope is capture-specific
// (usesAdditionalToolsEnvelope in the codex driver). That has only been done for
// Astra, and a codex mapping the backend refuses 400s every request.
//
// reliant and vertexai are absent for the reason astra's are: Vertex serves
// no OpenAI frontier models, and the gateway routes everything through it.
func TestGPT6NewModelProviderMappings(t *testing.T) {
	reg := MustGetRegistry()

	for id, want := range map[string]map[string]string{
		"gpt-6-sol":   {"openai": "gpt-6-sol", "openrouter": "openai/gpt-6-sol"},
		"gpt-6-terra": {"openai": "gpt-6-terra"}, // openrouter removed 2026-10-04: not served
		"gpt-6-luna":  {"openai": "gpt-6-luna", "openrouter": "openai/gpt-6-luna", "copilot": "gpt-6-luna"},
	} {
		assert.Equal(t, want, providerAPIModels(t, reg, id), "%s providers", id)
	}
}

// These models take tools only on the Responses API (Chat Completions refuses
// tool calling for Sol outright, and accepts it on Luna only at effort `none`),
// so the responses endpoint is a correctness setting, not a preference. They
// share the rest of the OpenAI reasoning-model envelope.
func TestGPT6NewModelCapabilities(t *testing.T) {
	reg := MustGetRegistry()

	for _, id := range []string{"gpt-6-sol", "gpt-6-terra", "gpt-6-luna"} {
		t.Run(id, func(t *testing.T) {
			def, ok := reg.GetDefinition(id)
			require.True(t, ok)

			assert.Equal(t, 1050000, def.Capabilities.MaxContextWindow)
			assert.Equal(t, 128000, def.Capabilities.MaxOutputTokens)
			assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, def.Capabilities.ThinkingLevels)
			assert.True(t, def.Capabilities.CanReason)
			assert.True(t, def.Capabilities.SupportsTools)
			assert.Equal(t, []string{"image", "text"}, def.Capabilities.SupportedFileTypes)

			assert.Equal(t, "responses", def.DriverSettings.PreferredEndpoint)
			assert.Equal(t, string(TemperatureModeOmit), def.DriverSettings.TemperatureMode)
			assert.True(t, def.DriverSettings.UseMaxCompletionTokens)
		})
	}
}

// The new models pin the intended tier winners for each provider set. Every row
// is a provider set whose winner must stay put unless a tier retune is explicit.
func TestSept2026ModelsPinTierWinners(t *testing.T) {
	reg := MustGetRegistry()

	tests := []struct {
		tag       string
		providers []string
		want      string
	}{
		{TagFlagship, allTestProviders, "claude-5.5-opus"},
		{TagModerate, allTestProviders, "claude-5.5-sonnet"},
		{TagReasoning, allTestProviders, "claude-5.5-opus"},
		{TagPowerful, allTestProviders, "claude-5.1-fable"},
		{TagFast, allTestProviders, "gemini-3.5-flash"},
		{TagCheap, allTestProviders, "claude-4.5-haiku"},

		{TagModerate, []string{"vertexai"}, "vertex-claude-5.5-sonnet"},
		{TagFlagship, []string{"vertexai"}, "claude-5.5-opus"},

		{TagFlagship, []string{"openai"}, "gpt-6-sol"},
		{TagModerate, []string{"openai"}, "gpt-6-terra"},
		{TagReasoning, []string{"openai"}, "gpt-5.5"},
		{TagPowerful, []string{"openai"}, "gpt-6-astra"},
		{TagFast, []string{"openai"}, "gpt-5.4-mini"},
		{TagCheap, []string{"openai"}, "gpt-5.4-mini"},

		{TagFlagship, []string{"copilot"}, "claude-5.5-sonnet"}, // 5.5 sits above 5 in flagship and copilot now serves it
	}

	for _, tt := range tests {
		t.Run(tt.tag+"/"+tt.providers[0], func(t *testing.T) {
			resolved, err := reg.Resolve(ModelSelector{Tags: []string{tt.tag}}, tt.providers)
			require.NoError(t, err)
			assert.Equal(t, tt.want, resolved.Definition.ID)
		})
	}
}

// Placement is behind the incumbents, but it must still be a REAL placement:
// each new model has to be the tier's pick once the models above it are not
// servable, and at the level the entry declares. Otherwise the tag entry is
// dead weight that no user ever resolves to.
func TestSept2026ModelsAreReachableThroughTheirTiers(t *testing.T) {
	reg := MustGetRegistry()

	tests := []struct {
		id    string
		tag   string
		level string
	}{
		// Sonnet 5.5 is the new moderate leader and stays directly above Sonnet 5
		// in the other tiers where Sonnet 5 appears.
		{"claude-5.5-sonnet", TagFlagship, "high"},
		{"claude-5.5-sonnet", TagModerate, "medium"},
		{"claude-5.5-sonnet", TagReasoning, "high"},
		{"vertex-claude-5.5-sonnet", TagFlagship, "high"},
		{"vertex-claude-5.5-sonnet", TagModerate, "medium"},
		{"vertex-claude-5.5-sonnet", TagReasoning, "high"},

		// GPT-6 tiers: Astra is powerful, Sol is flagship, Terra is moderate.
		{"gpt-6-astra", TagPowerful, "xhigh"},
		{"gpt-6-sol", TagFlagship, "xhigh"},
		{"gpt-6-sol", TagReasoning, "xhigh"},
		{"gpt-6-terra", TagModerate, "medium"},
		{"gpt-6-terra", TagReasoning, "medium"},

		// Luna is priced below every existing cheap/fast GPT entry, but it is
		// placed AFTER them: those tiers carry chat titling and compaction for
		// every OpenAI user, and a model nobody has run through them yet
		// should not become their default by virtue of being new.
		{"gpt-6-luna", TagFast, "low"},
		{"gpt-6-luna", TagCheap, "low"},
		{"gpt-6-luna", TagMeta, "low"},
	}

	for _, tt := range tests {
		t.Run(tt.id+"/"+tt.tag, func(t *testing.T) {
			assert.Equal(t, tt.level, entryLevel(t, reg, tt.tag, tt.id))
		})
	}
}

// Sonnet 5.5 is a Sonnet-class model: it must never carry `powerful`, which
// runs at xhigh by default and is where the frontier models live.
func TestClaude55SonnetIsNotPowerful(t *testing.T) {
	reg := MustGetRegistry()

	for _, id := range []string{"claude-5.5-sonnet", "vertex-claude-5.5-sonnet"} {
		assert.NotContains(t, reg.TagsOf(id), TagPowerful, id)
	}
}
