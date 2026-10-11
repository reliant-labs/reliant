// Copyright (c) 2025 Reliant Labs
package models

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// providerRetirement is one provider mapping its provider has shut down, or
// scheduled to, as the provider's own deprecation page publishes it.
type providerRetirement struct {
	model, driver string
	// retiredOn is the provider's published shutdown date.
	retiredOn string
	// replacement is the catalog id of the replacement the provider recommends.
	replacement string
}

// providerRetirements is every provider mapping the catalog carried that its
// provider has shut down or scheduled to shut down. Each is a `retired:` entry
// in definitions/models.yaml, so a chat pinned to the mapping keeps resolving —
// to the replacement, on the same provider — instead of 404ing from that date.
//
// Sources, checked 2026-10-11:
//
//	Gemini API  ai.google.dev/gemini-api/docs/deprecations
//	Vertex AI   docs.cloud.google.com/vertex-ai/generative-ai/docs/learn/model-versions
//	OpenAI      developers.openai.com/api/docs/deprecations
//
// Anthropic (platform.claude.com/docs/en/about-claude/model-deprecations) and
// GitHub Copilot (docs.github.com/en/copilot/reference/ai-models/supported-models,
// "Model retirement history") have retired nothing the catalog still maps.
var providerRetirements = []providerRetirement{
	// Gemini API: shut down 2026-03-09; recommended replacement
	// gemini-3.1-pro-preview. OpenRouter dropped it before that (2026-10-04
	// probe: 404 "No endpoints found"), and nothing else mapped it.
	{"gemini-3-pro-preview", "gemini", "2026-03-09", "gemini-3.1-pro-preview"},
	// Gemini API: shut down 2026-05-25. Google's row names gemini-3.1-flash-lite,
	// which is itself deprecated (shutdown 2027-05-07) with gemini-3.5-flash-lite
	// as ITS replacement. A retirement redirects one hop, so it names the end of
	// Google's chain directly. Only the gemini driver mapped it.
	{"gemini-3.1-flash-lite-preview", "gemini", "2026-05-25", "gemini-3.5-flash-lite"},
	// OpenAI API: shut down 2026-07-23; substitute gpt-5.6-sol. OpenRouter
	// still serves it from Azure, so only the openai mapping retires.
	{"gpt-5.2-codex", "openai", "2026-07-23", "gpt-5.6-sol"},
	// Vertex AI retires Gemini 2.5 on 2026-10-20 and names gemini-3.8-flash
	// first among the replacements for both. The managed gateway serves Gemini
	// from Vertex, so gemini-2.5-pro leaves `reliant` the same day.
	{"vertex-gemini-2.5-pro", "vertexai", "2026-10-20", "vertex-gemini-3.8-flash"},
	{"vertex-gemini-2.5-flash", "vertexai", "2026-10-20", "vertex-gemini-3.8-flash"},
	{"gemini-2.5-pro", "reliant", "2026-10-20", "gemini-3.8-flash"},
}

func isProviderRetired(model, driver string) bool {
	return slices.ContainsFunc(providerRetirements, func(r providerRetirement) bool {
		return r.model == model && r.driver == driver
	})
}

// Each shut-down mapping is a catalog retirement with the provider's date and
// replacement, and a chat pinned to it lands on that replacement, on the same
// provider.
func TestProviderRetirementsMoveToTheRecommendedReplacement(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	for _, want := range providerRetirements {
		selector := want.model + "@" + want.driver
		t.Run(selector, func(t *testing.T) {
			t.Parallel()
			retirement := reg.retirementOn(want.model, want.driver)
			require.NotNil(t, retirement, "%s is shut down; models.yaml needs a retired: entry for it", selector)
			assert.Equal(t, want.retiredOn, retirement.RetiredOn, "%s: the provider's published shutdown date", selector)
			assert.Equal(t, want.replacement, retirement.Replacement, "%s: the replacement the provider recommends", selector)

			if def, defined := reg.GetDefinition(want.model); defined {
				assert.False(t, mapsDriver(def, want.driver), "%s is shut down; %s must not map %s", selector, want.model, want.driver)
			}

			resolved, err := reg.Resolve(ModelSelector{ID: selector}, []string{want.driver})
			require.NoError(t, err, "a chat pinned to %s must keep resolving", selector)
			assert.Equal(t, want.replacement, resolved.Definition.ID)
			assert.Equal(t, want.driver, resolved.Provider.Driver, "the move must not change providers")
			// The send path posts its "model retired" notice from this.
			if assert.NotNil(t, resolved.Retirement, "%s must resolve AS a retirement", selector) {
				assert.Equal(t, want.retiredOn, resolved.Retirement.RetiredOn)
			}
		})
	}
}

// A retirement added to models.yaml without a row above would ship a date and
// a replacement nobody checked against the provider's page.
func TestEveryCatalogRetirementIsDocumented(t *testing.T) {
	t.Parallel()
	for model, retirements := range MustGetRegistry().retired {
		for _, retirement := range retirements {
			for _, driver := range retirement.Drivers {
				assert.True(t, isProviderRetired(model, driver),
					"models.yaml retires %s@%s but providerRetirements has no row for it; add the provider's date and replacement", model, driver)
			}
		}
	}
}

// No tier may route to a mapping its provider has shut down. A tag entry names
// a model, and resolution takes any provider of it the user has, so for a user
// whose only match is the dead mapping, every feature asking for that tier
// fails on every request. That is how the meta tier broke for Gemini-only
// users: titling, routing and summarizing all resolved to
// gemini-3.1-flash-lite-preview, which the Gemini API shut down on 2026-05-25.
func TestNoTierRoutesToARetiredMapping(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	for _, tag := range reg.ListAllTags() {
		for _, entry := range reg.TagEntries(tag) {
			def, ok := reg.GetDefinition(entry.Model)
			require.True(t, ok, "[%s] lists undefined model %s", tag, entry.Model)
			for _, provider := range def.Providers {
				assert.False(t, isProviderRetired(entry.Model, provider.Driver),
					"[%s] lists %s, which %s has shut down; replace the entry with its replacement, or drop that mapping",
					tag, entry.Model, provider.Driver)
			}
		}

		// The same, from a single-provider user's side: what the tier
		// actually resolves to. A tier a provider cannot serve at all is
		// TestEveryProviderImplementsEveryCoreTag's concern, not this one's.
		for _, driver := range allTestProviders {
			resolved, err := reg.Resolve(ModelSelector{Tags: []string{tag}}, []string{driver})
			if err != nil {
				continue
			}
			assert.False(t, isProviderRetired(resolved.Definition.ID, resolved.Provider.Driver),
				"[%s] resolves to %s@%s for a %s-only user, a mapping its provider has shut down",
				tag, resolved.Definition.ID, resolved.Provider.Driver, driver)
		}
	}
}

// A Gemini-only user's meta and cheap tiers run on gemini-3.5-flash-lite, the
// shut-down flash-lite preview's replacement. Dropping the preview without it
// would not fail — the tiers fall through to gemini-3.8-flash — but then every
// chat title and summary runs on the Gemini flagship.
func TestGeminiOnlyMetaAndCheapTiersRunOnFlashLite(t *testing.T) {
	t.Parallel()
	reg := MustGetRegistry()

	for _, tag := range []string{TagMeta, TagCheap} {
		resolved, err := reg.Resolve(ModelSelector{Tags: []string{tag}}, []string{"gemini"})
		require.NoError(t, err, tag)
		assert.Equal(t, "gemini-3.5-flash-lite", resolved.Definition.ID, "[%s] for a Gemini-only user", tag)
		assert.Equal(t, "gemini", resolved.Provider.Driver, tag)
		assert.Equal(t, "high", resolved.ThinkingLevel, "[%s]: no tier entry runs below high (#649)", tag)
	}
}

// Only the shut-down mapping retires: OpenRouter still serves gpt-5.2-codex
// (an Azure endpoint, probed 2026-10-11), so a chat pinned there stays put.
func TestGPT52CodexStaysOnOpenRouter(t *testing.T) {
	t.Parallel()
	resolved, err := MustGetRegistry().Resolve(ModelSelector{ID: "gpt-5.2-codex@openrouter"}, []string{"openrouter", "openai"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.2-codex", resolved.Definition.ID)
	assert.Equal(t, "openrouter", resolved.Provider.Driver)
	assert.Nil(t, resolved.Retirement)
}
