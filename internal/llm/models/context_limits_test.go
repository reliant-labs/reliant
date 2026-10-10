// Copyright (c) 2025 Reliant Labs
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalogContextLimits is every text model × provider in models.yaml with the
// context limits that provider publishes: the TOTAL context window (input +
// output) and the max output. The prompt ceiling reliant enforces is
// window − max output (PromptCeiling), so both numbers matter.
//
// Each row cites its source, checked 2026-10-09 and re-checked 2026-10-10:
//   - Anthropic <model>: platform.claude.com/docs/en/models/<model>/overview —
//     1M / 128K for Claude 4.6+ (1M is the default, no beta header), 200K / 64K
//     for the 4.5 generation. The Claude subscription and the reliant gateway
//     serve the same limits.
//   - Vertex …: cloud.google.com/vertex-ai/generative-ai/docs/{partner-models/claude,models/gemini}/<model>.
//   - OpenAI <id>: developers.openai.com/api/docs/models/<id>.md.
//   - OpenRouter: openrouter.ai/api/v1/models, context_length and
//     top_provider.max_completion_tokens. Machine-checked against the capture by
//     TestCatalogOpenRouterLimitsMatchCapture (internal/llm/drivers/openrouter).
//   - Gemini API <id>: ai.google.dev/gemini-api/docs/models/<id>. Google
//     publishes an INPUT limit of 1,048,576 with a separate 65,536 output; it is
//     kept as the window, which errs 65,536 tokens conservative.
//   - Copilot: GET /models, internal/llm/drivers/copilot/testdata/copilot_models.json
//     (max_context_window_tokens / max_output_tokens; max_prompt_tokens is the
//     live advertised limit). Machine-checked by
//     TestCatalogCopilotLimitsMatchCapture (internal/llm/drivers/copilot).
//   - codex: OpenAI's published window and output. /codex/models'
//     max_context_window (872,000; 272,000 for gpt-5.5) caps the ceiling live,
//     and TestCodexCatalogCeilingCoversAdvertisedLimit
//     (internal/llm/drivers/codex) keeps the catalog-only ceiling at or above it.
var catalogContextLimits = []struct {
	model, driver     string
	window, maxOutput int
	source            string
}{
	{"claude-5.5-opus", "anthropic", 1000000, 128000, "Anthropic opus-5-5"},
	{"claude-5.5-opus", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-5.5-opus", "reliant", 1000000, 128000, "Anthropic opus-5-5 (gateway)"},
	{"claude-5.5-opus", "vertexai", 1000000, 128000, "Vertex claude/opus-5-5"},
	{"claude-5-opus", "anthropic", 1000000, 128000, "Anthropic opus-5"},
	{"claude-5-opus", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-5-opus", "reliant", 1000000, 128000, "Anthropic opus-5 (gateway)"},
	{"claude-5.1-fable", "anthropic", 1000000, 128000, "Anthropic fable-5-1"},
	{"claude-5.1-fable", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-5.1-fable", "reliant", 1000000, 128000, "Anthropic fable-5-1 (gateway)"},
	{"claude-5.1-fable", "vertexai", 1000000, 128000, "Vertex claude/fable-5-1"},
	{"claude-4.8-opus", "anthropic", 1000000, 128000, "Anthropic opus-4-8"},
	{"claude-4.8-opus", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-5.5-sonnet", "anthropic", 1000000, 128000, "Anthropic sonnet-5-5"},
	{"claude-5.5-sonnet", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-5.5-sonnet", "reliant", 1000000, 128000, "Anthropic sonnet-5-5 (gateway)"},
	{"claude-5.5-sonnet", "copilot", 1000000, 128000, "Copilot (prompt 936,000)"},
	{"claude-5-sonnet", "anthropic", 1000000, 128000, "Anthropic sonnet-5"},
	{"claude-5-sonnet", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-5-sonnet", "copilot", 1000000, 64000, "Copilot (prompt 936,000)"},
	{"claude-5-fable", "anthropic", 1000000, 128000, "Anthropic fable-5"},
	{"claude-5-fable", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-4.6-opus", "anthropic", 1000000, 128000, "Anthropic opus-4-6"},
	{"claude-4.6-opus", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-4.6-opus", "reliant", 1000000, 128000, "Anthropic opus-4-6 (gateway)"},
	{"claude-4.5-opus", "anthropic", 200000, 64000, "Anthropic opus-4-5"},
	{"claude-4.5-opus", "openrouter", 200000, 64000, "OpenRouter"},
	{"claude-4.5-opus", "reliant", 200000, 64000, "Anthropic opus-4-5 (gateway)"},
	{"gpt-5.5", "codex", 400000, 128000, "/codex/models 272,000 + 128,000 output (#663)"},
	{"gpt-5.5", "openai", 1050000, 128000, "OpenAI gpt-5.5"},
	{"gpt-5.5", "openrouter", 1050000, 128000, "OpenRouter"},
	{"gpt-6.1-astra", "openai", 1050000, 128000, "UNVERIFIED: OpenAI page 404 on 2026-10-09; catalog value kept"},
	{"gpt-6-astra", "codex", 1050000, 128000, "OpenAI gpt-6-astra; /codex/models 872,000 caps the ceiling (#663)"},
	{"gpt-6-astra", "openai", 1050000, 128000, "OpenAI gpt-6-astra"},
	{"gpt-6-astra", "openrouter", 1050000, 128000, "OpenRouter"},
	{"gpt-6.1-sol", "codex", 1050000, 128000, "OpenAI gpt-6.1-sol; /codex/models 872,000 caps the ceiling"},
	{"gpt-6.1-sol", "openai", 1050000, 128000, "OpenAI gpt-6.1-sol"},
	{"gpt-6-sol", "codex", 1050000, 128000, "OpenAI gpt-6-sol; /codex/models 872,000 caps the ceiling"},
	{"gpt-6-sol", "openai", 1050000, 128000, "OpenAI gpt-6-sol"},
	{"gpt-6-sol", "openrouter", 1050000, 128000, "OpenRouter"},
	{"gpt-6.1-terra", "openai", 1050000, 128000, "UNVERIFIED: OpenAI page 404 on 2026-10-09; catalog value kept"},
	{"gpt-6-terra", "openai", 1050000, 128000, "UNVERIFIED: OpenAI page 404 on 2026-10-09; catalog value kept"},
	{"gpt-6-luna", "codex", 1050000, 128000, "OpenAI gpt-6-luna; /codex/models 872,000 caps the ceiling"},
	{"gpt-6-luna", "openai", 1050000, 128000, "OpenAI gpt-6-luna"},
	{"gpt-6-luna", "openrouter", 1050000, 128000, "OpenRouter"},
	{"gpt-6-luna", "copilot", 1000000, 128000, "Copilot (prompt 872,000)"},
	{"gpt-5.6-sol", "codex", 1050000, 128000, "OpenAI gpt-5.6-sol; /codex/models 872,000 caps the ceiling (#663)"},
	{"gpt-5.6-terra", "codex", 1050000, 128000, "OpenAI gpt-5.6-terra; /codex/models 872,000 caps the ceiling (#663)"},
	{"gpt-5.6-terra", "copilot", 1050000, 128000, "Copilot (prompt 922,000)"},
	{"gpt-5.6-luna", "codex", 1050000, 128000, "OpenAI gpt-5.6-luna; /codex/models 872,000 caps the ceiling (#663)"},
	{"gpt-5.6-luna", "copilot", 1050000, 128000, "Copilot (prompt 922,000)"},
	{"gpt-5.4", "openai", 1050000, 128000, "OpenAI gpt-5.4"},
	{"gpt-5.4", "openrouter", 1050000, 128000, "OpenRouter"},
	{"gpt-5.4", "copilot", 1050000, 128000, "Copilot (prompt 922,000)"},
	{"gpt-5.4-pro", "openai", 1050000, 128000, "OpenAI gpt-5.4-pro"},
	{"gpt-5.3-codex", "openai", 400000, 128000, "OpenAI gpt-5.3-codex"},
	{"gpt-5.3-codex", "openrouter", 400000, 128000, "OpenRouter"},
	{"gpt-5.3-codex", "copilot", 400000, 128000, "Copilot (prompt 272,000)"},
	{"gpt-5.2-codex", "openai", 400000, 128000, "OpenAI gpt-5.2-codex"},
	{"gpt-5.2-codex", "openrouter", 400000, 128000, "OpenRouter"},
	{"gemini-3.8-flash", "gemini", 1048576, 65536, "Gemini API gemini-3.8-flash"},
	{"gemini-3.8-flash", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-3.8-flash", "reliant", 1048576, 65536, "Gemini API gemini-3.8-flash (gateway)"},
	{"gemini-3.8-flash", "antigravity", 1048576, 65536, "UNPUBLISHED: Antigravity states no limits; Gemini API values, capture sends maxOutputTokens 65,536"},
	{"gemini-3.8-flash", "copilot", 1048576, 65536, "Copilot (prompt 983,040)"},
	{"gemini-3.7-flash", "gemini", 1048576, 65536, "Gemini API gemini-3.7-flash"},
	{"gemini-3.7-flash", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-3.7-flash", "reliant", 1048576, 65536, "Gemini API gemini-3.7-flash (gateway)"},
	{"gemini-3.7-flash", "copilot", 1000000, 64000, "Copilot (prompt 936,000)"},
	{"gemini-3.1-pro-preview", "gemini", 1048576, 65536, "Gemini API gemini-3.1-pro-preview"},
	{"gemini-3.1-pro-preview", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-3.1-pro-preview", "reliant", 1048576, 65536, "Gemini API gemini-3.1-pro-preview (gateway)"},
	{"gemini-3-pro-preview", "gemini", 1048576, 65536, "Gemini API gemini-3-pro-preview"},
	{"gemini-3.1-pro-preview-customtools", "gemini", 1048576, 65536, "Gemini API gemini-3.1-pro-preview-customtools"},
	{"claude-4.6-sonnet", "anthropic", 1000000, 128000, "Anthropic sonnet-4-6"},
	{"claude-4.6-sonnet", "openrouter", 1000000, 128000, "OpenRouter"},
	{"claude-4.6-sonnet", "reliant", 1000000, 128000, "Anthropic sonnet-4-6 (gateway)"},
	{"claude-4.5-sonnet", "anthropic", 200000, 64000, "Anthropic sonnet-4-5; 1M needs a context-1m header no driver sends"},
	{"claude-4.5-sonnet", "openrouter", 1000000, 64000, "OpenRouter"},
	{"claude-4.5-sonnet", "reliant", 200000, 64000, "Anthropic sonnet-4-5 (gateway); 1M needs a context-1m header"},
	{"gpt-5.2-pro", "openai", 400000, 128000, "OpenAI gpt-5.2-pro"},
	{"gpt-5.2-pro", "openrouter", 400000, 128000, "OpenRouter"},
	{"gpt-5.2", "openai", 400000, 128000, "OpenAI gpt-5.2"},
	{"gpt-5.2", "openrouter", 400000, 128000, "OpenRouter"},
	{"gemini-3.6-flash", "gemini", 1048576, 65536, "Gemini API gemini-3.6-flash"},
	{"gemini-3.6-flash", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-3.5-flash", "gemini", 1048576, 65536, "Gemini API gemini-3.5-flash"},
	{"gemini-3.5-flash", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-2.5-pro", "gemini", 1048576, 65536, "Gemini API gemini-2.5-pro"},
	{"gemini-2.5-pro", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-2.5-pro", "reliant", 1048576, 65536, "Gemini API gemini-2.5-pro (gateway)"},
	{"claude-4.5-haiku", "anthropic", 200000, 64000, "Anthropic haiku-4-5"},
	{"claude-4.5-haiku", "openrouter", 200000, 64000, "OpenRouter"},
	{"claude-4.5-haiku", "reliant", 200000, 64000, "Anthropic haiku-4-5 (gateway)"},
	{"claude-4.5-haiku", "copilot", 144000, 32000, "Copilot (prompt 128,000)"},
	{"gpt-5.4-mini", "openai", 400000, 128000, "OpenAI gpt-5.4-mini"},
	{"gpt-5.4-mini", "openrouter", 400000, 128000, "OpenRouter"},
	{"gpt-5.4-mini", "copilot", 400000, 128000, "Copilot (prompt 272,000)"},
	{"gpt-5-mini", "copilot", 264000, 64000, "Copilot (prompt 128,000; OpenAI publishes 400,000 / 128,000)"},
	{"gemini-3-flash-preview", "gemini", 1048576, 65536, "Gemini API gemini-3-flash-preview"},
	{"gemini-3-flash-preview", "openrouter", 1048576, 65536, "OpenRouter"},
	{"gemini-3-flash-preview", "reliant", 1048576, 65536, "Gemini API gemini-3-flash-preview (gateway)"},
	{"gemini-3.1-flash-lite-preview", "gemini", 1048576, 65536, "Gemini API gemini-3.1-flash-lite-preview"},
	{"gemini-2.5-flash", "gemini", 1048576, 65536, "Gemini API gemini-2.5-flash"},
	{"gemini-2.5-flash", "openrouter", 1048576, 65535, "OpenRouter max_completion_tokens 65,535 (Google publishes 65,536)"},
	{"gemini-2.5-flash-lite", "gemini", 1048576, 65536, "Gemini API gemini-2.5-flash-lite"},
	{"gemini-2.5-flash-lite", "openrouter", 1048576, 65535, "OpenRouter max_completion_tokens 65,535 (Google publishes 65,536)"},
	{"grok-4.5", "copilot", 500000, 128000, "Copilot (prompt 372,000)"},
	{"grok-4.6", "copilot", 500000, 128000, "Copilot (prompt 372,000)"},
	{"grok-4.7", "copilot", 500000, 128000, "Copilot (prompt 372,000)"},
	{"kimi-k3", "copilot", 1048576, 131072, "Copilot (prompt 917,504)"},
	{"mai-code-1.1-flash", "copilot", 256000, 128000, "Copilot (prompt 128,000)"},
	{"gpt-5.4-nano", "copilot", 400000, 128000, "Copilot (prompt 272,000)"},
	{"vertex-claude-5.5-opus", "vertexai", 1000000, 128000, "Vertex claude/opus-5-5"},
	{"vertex-claude-5-opus", "vertexai", 1000000, 128000, "Vertex claude/opus-5"},
	{"vertex-claude-4.8-opus", "vertexai", 1000000, 128000, "Vertex claude/opus-4-8"},
	{"vertex-claude-5.5-sonnet", "vertexai", 1000000, 128000, "Vertex claude/sonnet-5-5"},
	{"vertex-claude-5-sonnet", "vertexai", 1000000, 128000, "Vertex claude/sonnet-5"},
	{"vertex-claude-5.1-fable", "vertexai", 1000000, 128000, "Vertex claude/fable-5-1"},
	{"vertex-claude-5-fable", "vertexai", 1000000, 128000, "Vertex claude/fable-5"},
	{"vertex-claude-4.6-opus", "vertexai", 1000000, 128000, "Vertex claude/opus-4-6"},
	{"vertex-claude-4.5-sonnet", "vertexai", 200000, 64000, "Vertex claude/sonnet-4-5 (200K GA; the 1M preview needs context-1m, not sent)"},
	{"vertex-claude-4.5-haiku", "vertexai", 200000, 64000, "Vertex claude/haiku-4-5"},
	{"vertex-gemini-2.5-pro", "vertexai", 1048576, 65536, "Vertex gemini/2-5-pro"},
	{"vertex-gemini-2.5-flash", "vertexai", 1048576, 65536, "Vertex gemini/2-5-flash"},
}

// TestCatalogContextLimitsMatchProviders pins the window and max output every
// provider serves each model with to that provider's published figures. A
// copy-pasted or stale value — the Claude 4.6 generation sat at 200k / 20k
// against a published 1M / 128K — fails here with its source named.
func TestCatalogContextLimitsMatchProviders(t *testing.T) {
	reg := MustGetRegistry()
	for _, w := range catalogContextLimits {
		def, ok := reg.GetDefinition(w.model)
		require.True(t, ok, "%s is not in the catalog", w.model)
		assert.True(t, hasProvider(def, w.driver), "%s has no %s provider", w.model, w.driver)
		assert.Equal(t, w.window, EffectiveContextWindow(def, w.driver), "%s@%s window (source: %s)", w.model, w.driver, w.source)
		assert.Equal(t, w.maxOutput, EffectiveMaxOutputTokens(def, w.driver), "%s@%s max output (source: %s)", w.model, w.driver, w.source)
	}
}

// TestCatalogContextLimitsCoverEveryProvider makes the table above the gate for
// new entries: a text model or provider mapping added to models.yaml without a
// sourced row fails here, so nobody ships a window nobody checked.
func TestCatalogContextLimitsCoverEveryProvider(t *testing.T) {
	pinned := map[string]bool{}
	for _, w := range catalogContextLimits {
		pinned[w.model+"@"+w.driver] = true
	}
	for _, def := range MustGetRegistry().ListAll() {
		if def.Visibility == VisibilityDev || len(def.Capabilities.OutputModalities) > 0 || def.Capabilities.MaxContextWindow <= 0 {
			continue
		}
		for _, p := range def.Providers {
			assert.True(t, pinned[def.ID+"@"+p.Driver],
				"%s@%s has no row in catalogContextLimits: add one with the provider's published window and max output and cite it", def.ID, p.Driver)
		}
	}
}

func hasProvider(def *ModelDefinition, driver string) bool {
	for _, p := range def.Providers {
		if p.Driver == driver {
			return true
		}
	}
	return false
}

// A provider's max output override only ever lowers the model-wide value, and
// it is what the prompt ceiling reserves on that provider.
func TestEffectiveMaxOutputTokens(t *testing.T) {
	def := &ModelDefinition{
		Capabilities: ModelCapabilities{MaxContextWindow: 1_000_000, MaxOutputTokens: 128_000},
		Providers: []ProviderMapping{
			{Driver: "anthropic", APIModel: "m"},
			{Driver: "copilot", APIModel: "m", MaxOutputTokens: 64_000},
			{Driver: "bigger", APIModel: "m", MaxOutputTokens: 256_000},
		},
	}
	assert.Equal(t, 128_000, EffectiveMaxOutputTokens(def, ""))
	assert.Equal(t, 128_000, EffectiveMaxOutputTokens(def, "anthropic"))
	assert.Equal(t, 64_000, EffectiveMaxOutputTokens(def, "copilot"))
	assert.Equal(t, 128_000, EffectiveMaxOutputTokens(def, "bigger"), "an override above the model-wide value is ignored")
	assert.Equal(t, 0, EffectiveMaxOutputTokens(nil, "copilot"))

	assert.Equal(t, 872_000, ProviderPromptCeiling(def, "anthropic"))
	assert.Equal(t, 936_000, ProviderPromptCeiling(def, "copilot"), "the ceiling reserves the provider's output, not the model-wide one")
}

// The per-provider limits reach the picker: ModelsForDriver reports what that
// provider serves.
func TestModelsForDriverReportsProviderLimits(t *testing.T) {
	reg := MustGetRegistry()
	find := func(driver, id string) ModelInfo {
		for _, mi := range reg.ModelsForDriver(driver) {
			if mi.ID == id {
				return mi
			}
		}
		t.Fatalf("%s not served by %s", id, driver)
		return ModelInfo{}
	}
	copilot := find("copilot", "claude-4.5-haiku")
	assert.Equal(t, 144000, copilot.Capabilities.MaxContextWindow)
	assert.Equal(t, 32000, copilot.Capabilities.MaxOutputTokens)

	anthropic := find("anthropic", "claude-4.5-haiku")
	assert.Equal(t, 200000, anthropic.Capabilities.MaxContextWindow)
	assert.Equal(t, 64000, anthropic.Capabilities.MaxOutputTokens)
}

// Copilot advertises max_prompt_tokens. Where that is below window − max output
// (gpt-5-mini: 264,000 − 64,000 = 200,000 against a 128,000 cap) it is the
// ceiling once resolution stamps it on.
func TestCopilotAdvertisedPromptCapLowersCeiling(t *testing.T) {
	reg := MustGetRegistry().WithAvailability(func(driver, modelID string) ModelAvailability {
		if driver == "copilot" && modelID == "gpt-5-mini" {
			return ModelAvailability{ContextWindow: 128000}
		}
		return ModelAvailability{}
	})
	resolved, err := reg.Resolve(ModelSelector{ID: "gpt-5-mini"}, []string{"copilot"})
	require.NoError(t, err)
	assert.Equal(t, 200000, PromptCeiling(EffectiveContextWindow(&resolved.Definition, "copilot"), EffectiveMaxOutputTokens(&resolved.Definition, "copilot")))
	assert.Equal(t, 128000, ProviderPromptCeiling(&resolved.Definition, "copilot"))
}
