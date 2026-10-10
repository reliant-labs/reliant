// Copyright (c) 2025 Reliant Labs
package codex

import (
	"os"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testdata/codex_models.json is the model catalog Codex CLI bundles —
// openai/codex codex-rs/models-manager/models.json at d63a9b8344 (2026-10-06,
// fetched 2026-10-10; gpt-6.1-sol arrived in b1e72963c3 on 2026-09-29) —
// projected onto the fields this package and its tests read. Codex CLI
// deserializes that file and GET /codex/models into the same type
// (codex_protocol::openai_models::ModelsResponse), so it is a /codex/models
// body. The 2026-10-04 live recording it replaced listed the same windows and
// levels for every model both carry. Refresh with:
//
//	gh api repos/openai/codex/contents/codex-rs/models-manager/models.json \
//	  -H 'Accept: application/vnd.github.raw' | jq --indent 1 '{models: [.models[] |
//	  {slug, display_name, description, visibility, priority, minimal_client_version,
//	   context_window, max_context_window, default_reasoning_level,
//	   supported_reasoning_levels, supported_in_api, tool_mode, use_responses_lite,
//	   default_service_tier}]}'
//
// then run this package's tests: catalog_drift_test.go names every listed model
// reliant does not serve yet.
func recordedReport(t *testing.T) registry.ProviderAvailability {
	t.Helper()
	body, err := os.ReadFile("testdata/codex_models.json")
	require.NoError(t, err)
	report, err := parseCodexModels(body)
	require.NoError(t, err)
	return report
}

func TestParseCodexModels_ReasoningLevelsAreIntersectedWithAcceptedSet(t *testing.T) {
	report := recordedReport(t)

	// The catalog advertises `ultra` for astra/sol/terra; the API 400s on it.
	for _, slug := range []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra"} {
		got := report.Models[slug].ThinkingLevels
		assert.NotContains(t, got, "ultra", slug)
		assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, got, slug)
	}
	assert.Equal(t, []string{"low", "medium", "high", "xhigh"}, report.Models["gpt-5.5"].ThinkingLevels)
}

// /codex/models reports two numbers per model: context_window (272000, the
// window Codex CLI runs at by default) and max_context_window (872000, the most
// it lets a session configure). The backend's limit is max_context_window: prod
// accepted 698,604-token gpt-5.6-terra prompts.
func TestParseCodexModels_ReadsMaxContextWindow(t *testing.T) {
	report := recordedReport(t)
	for _, slug := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"} {
		assert.Equal(t, 872000, report.Models[slug].ContextWindow, slug)
	}
	assert.Equal(t, 272000, report.Models["gpt-5.5"].ContextWindow)
}

// A response without max_context_window falls back to context_window.
func TestParseCodexModels_FallsBackToContextWindow(t *testing.T) {
	report, err := parseCodexModels([]byte(`{"models":[
		{"slug":"gpt-5.6-terra","visibility":"list","context_window":300000,"supported_reasoning_levels":[{"effort":"high"}]}
	]}`))
	require.NoError(t, err)
	assert.Equal(t, 300000, report.For("gpt-5.6-terra").ContextWindow)
}

// The recorded report caps the codex prompt ceiling at the advertised 872000,
// below the catalog's 1,050,000 − 128,000 = 922,000; a lower account limit
// lowers it further.
func TestParseCodexModels_AdvertisedLimitCapsPromptCeiling(t *testing.T) {
	resolveSol := func(report registry.ProviderAvailability) int {
		t.Helper()
		avail := registry.BuildAvailabilityFunc(models.MustGetRegistry(), map[string]registry.ProviderAvailability{"codex": report})
		got, err := models.MustGetRegistry().WithAvailability(avail).Resolve(models.ModelSelector{ID: "gpt-5.6-sol@codex"}, []string{"codex"})
		require.NoError(t, err)
		return models.ProviderPromptCeiling(&got.Definition, "codex")
	}

	report := recordedReport(t)
	assert.Equal(t, 872000, resolveSol(report))

	custom := report
	custom.Models = map[string]models.ModelAvailability{"gpt-5.6-sol": {ContextWindow: 100000}}
	assert.Equal(t, 100000, resolveSol(custom))
}

func TestParseCodexModels_HiddenAndUnknownSlugsAreNotOffered(t *testing.T) {
	report := recordedReport(t)

	// visibility:"hide" slugs have no catalog entry and are never auto-added.
	for _, hidden := range []string{"gpt-daybreak-blue-latest", "gpt-daybreak-red-latest", "codex-auto-review"} {
		assert.NotContains(t, report.Models, hidden)
	}

	// Authoritative: a catalog model the account does not list is not servable.
	assert.True(t, report.Authoritative)
	assert.True(t, report.For("gpt-5.2-codex").Disabled)
	assert.False(t, report.For("gpt-5.5").Disabled)
}

func TestParseCodexModels_HideDisablesACatalogModel(t *testing.T) {
	report, err := parseCodexModels([]byte(`{"models":[
		{"slug":"gpt-5.5","visibility":"hide","context_window":272000,"supported_reasoning_levels":[{"effort":"low"}]},
		{"slug":"gpt-5.6-terra","visibility":"list","context_window":200000,"supported_reasoning_levels":[{"effort":"ultra"},{"effort":"high"}]}
	]}`))
	require.NoError(t, err)
	assert.True(t, report.For("gpt-5.5").Disabled)
	assert.False(t, report.For("gpt-5.6-terra").Disabled)
	assert.Equal(t, []string{"high"}, report.For("gpt-5.6-terra").ThinkingLevels)
	assert.Equal(t, 200000, report.For("gpt-5.6-terra").ContextWindow)
}

func TestParseCodexModels_UnknownSlugIsLoggedOnceNotAdded(t *testing.T) {
	unknownSlugsLogged.Delete("gpt-9-future")
	body := []byte(`{"models":[{"slug":"gpt-9-future","visibility":"list","context_window":1,"supported_reasoning_levels":[{"effort":"low"}]}]}`)

	first, err := parseCodexModels(body)
	require.NoError(t, err)
	assert.NotContains(t, first.Models, "gpt-9-future")
	_, logged := unknownSlugsLogged.Load("gpt-9-future")
	assert.True(t, logged, "unknown slug must be recorded as logged")

	_, err = parseCodexModels(body)
	require.NoError(t, err)
}

// The listing every catalog codex model must ride is the accepted set: the
// intersected levels can never contain a level the API rejects.
func TestCodexReportNeverContainsRejectedLevel(t *testing.T) {
	for slug, a := range recordedReport(t).Models {
		for _, level := range a.ThinkingLevels {
			assert.Contains(t, models.CodexAcceptedThinkingLevels(), level, slug)
		}
	}
}
