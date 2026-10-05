// Copyright (c) 2025 Reliant Labs
// Regression tests for which MODEL titles a chat.
//
// Titling used to be hard-pinned to Claude Haiku. A user who had configured
// only OpenAI (or only Gemini, or only Codex) therefore could not title at all:
// driver resolution failed, the activity returned an error, and the workflow
// eventually settled for the truncated first message. Every chat looked
// hand-titled, so the failure was invisible.
//
// Selection now goes through the "fast" tag, resolved against the providers the
// user actually has. These tests pin that a fast model exists for each provider
// in isolation, and that an Anthropic user's result is unchanged.
package handlers

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// titleProviders is every provider a user can configure and which the catalog
// serves at least one model for. Each must be able to title a chat on its own.
var titleProviders = []string{
	"anthropic", "openai", "gemini", "codex", "openrouter",
	"copilot", "antigravity", "reliant", "vertexai",
}

// resolveTitleModel runs the production ladder exactly as resolveLLMCall does:
// the primary selector, falling back to the secondary only when the primary
// cannot resolve at all. Restating it as a single multi-tag selector would test
// the bug this replaced.
func resolveTitleModel(t *testing.T, providers ...string) *models.ResolvedModel {
	t.Helper()
	resolved, err := models.MustGetRegistry().ResolveWithFallback(
		titleModelSelector(), titleModelFallbackSelector(), providers)
	require.NoError(t, err)
	return resolved
}

// These call the PRODUCTION selector rather than restating it, so a change
// back to a named model fails them instead of quietly passing.
func TestTitleModel_SelectsByTagNotAHardcodedModel(t *testing.T) {
	selector := titleModelSelector()
	assert.Empty(t, selector.ID,
		"titling must not name a model: a user without that model's provider cannot title at all")

	// A single tag, with [moderate] behind it as a STRICT fallback rather than
	// a second preference in the same selector. Multi-tag scoring sums weights,
	// so [fast, moderate] ranked a fast+moderate model above a fast-only one —
	// titling would have picked gpt-5.6-terra over gpt-5.4-mini.
	assert.Equal(t, []string{models.TagFast}, selector.Tags)
	assert.Equal(t, []string{models.TagModerate}, titleModelFallbackSelector().Tags)
	assert.Equal(t, models.ModalityText, titleModelFallbackSelector().RequireOutputModality)

	// The load-bearing half. Tags degrade gracefully and `fast` is carried by
	// image-generation models (gpt-image-2.5-flare, gemini-3.1-flash-lite-
	// image), so without a hard modality requirement titling can resolve to a
	// model that cannot emit text. Verified: bare TagFast on codex resolves to
	// gpt-image-2.5-flare.
	assert.Equal(t, models.ModalityText, selector.RequireOutputModality,
		"titling must REQUIRE text output; speed is only a preference")
}

// The regression that motivated RequireOutputModality: for every provider, the
// titling selector must land on a model that can actually emit text. Tag
// scoring alone cannot guarantee this, because image models carry latency tags
// too.
func TestTitleModel_NeverResolvesToAnImageModel(t *testing.T) {
	for _, provider := range titleProviders {
		t.Run(provider, func(t *testing.T) {
			resolved := resolveTitleModel(t, provider)
			assert.Truef(t, resolved.Definition.Capabilities.CanOutput(models.ModalityText),
				"titling for %s resolved to %q, which emits %v — it cannot produce a title",
				provider, resolved.Definition.ID,
				resolved.Definition.Capabilities.EffectiveOutputModalities())
		})
	}
}

// The bug this fixes: a user with no Anthropic credentials must still get a
// real model rather than falling back to the truncated first message.
func TestTitleModel_ResolvesForEachProviderAlone(t *testing.T) {
	for _, provider := range titleProviders {
		t.Run(provider, func(t *testing.T) {
			resolved := resolveTitleModel(t, provider)
			assert.Equal(t, provider, resolved.Provider.Driver,
				"must resolve through the provider the user actually configured")
			assert.NotEmpty(t, resolved.Definition.ID)
		})
	}
}

// The compatibility property, asserted rather than assumed: definition order in
// models.yaml puts claude-4.5-haiku first among fast models, so an Anthropic
// user keeps the exact model titling used before this change.
func TestTitleModel_AnthropicUserStillGetsHaiku(t *testing.T) {
	assert.Equal(t, string(models.Claude45Haiku), resolveTitleModel(t, "anthropic").Definition.ID)
}

// Which model titles, per provider set. Pinned because the answer is invisible
// when wrong — a title still appears, it just cost a reasoning-tier request.
//
// The anthropic+antigravity and openai rows are the regression that made this a
// strict ladder: under the old [fast, moderate] selector, gemini-3.8-flash and
// gpt-5.6-terra each carry BOTH tags, so their summed weight beat the
// fast-only Haiku and gpt-5.4-mini once antigravity and codex gained fast
// coverage. The last two rows are the gap that started it — neither provider
// could title at all.
func TestTitleModel_PinsTheModelPerProvider(t *testing.T) {
	for _, tt := range []struct {
		name      string
		providers []string
		wantModel models.ModelID
	}{
		{"anthropic and antigravity", []string{"anthropic", "antigravity"}, models.Claude45Haiku},
		{"openai", []string{"openai"}, models.GPT54Mini},
		{"antigravity only", []string{"antigravity"}, models.Gemini38Flash},
		{"codex only", []string{"codex"}, models.GPT56Terra},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, string(tt.wantModel), resolveTitleModel(t, tt.providers...).Definition.ID)
		})
	}
}

// Titling is a one-shot request that must not spend a reasoning budget, and it
// must be able to call a tool at all. A fast model that cannot use tools could
// never emit set_title.
func TestTitleModel_SupportsTools(t *testing.T) {
	for _, provider := range titleProviders {
		t.Run(provider, func(t *testing.T) {
			resolved := resolveTitleModel(t, provider)
			assert.True(t, resolved.Definition.Capabilities.SupportsTools,
				"%s resolves %s for titling, which cannot call set_title without tool support",
				provider, resolved.Definition.ID)
		})
	}
}

// No configured providers means no title model. The activity must surface that
// as an error rather than silently picking something the user cannot call.
func TestTitleModel_NoProvidersIsAnError(t *testing.T) {
	_, err := models.MustGetRegistry().ResolveWithFallback(
		titleModelSelector(), titleModelFallbackSelector(), nil)
	require.Error(t, err)
}
