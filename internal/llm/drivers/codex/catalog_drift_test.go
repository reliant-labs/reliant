// Copyright (c) 2025 Reliant Labs
package codex

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests hold reliant's codex mappings to the catalog Codex itself ships
// (testdata/codex_models.json; provenance on recordedReport). When OpenAI adds
// a model to that catalog, refreshing the capture makes them fail until the
// model is reachable through the codex driver — rather than a user noticing
// that the picker still shows last month's models.

// capturedCodexModel is the part of a /codex/models entry these tests compare.
type capturedCodexModel struct {
	Slug                 string `json:"slug"`
	Visibility           string `json:"visibility"`
	MinimalClientVersion string `json:"minimal_client_version"`
	UseResponsesLite     bool   `json:"use_responses_lite"`
}

func listedCodexModels(t *testing.T) []capturedCodexModel {
	t.Helper()
	body, err := os.ReadFile("testdata/codex_models.json")
	require.NoError(t, err)
	var parsed struct {
		Models []capturedCodexModel `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))

	var listed []capturedCodexModel
	for _, m := range parsed.Models {
		if m.Visibility == "list" {
			listed = append(listed, m)
		}
	}
	require.NotEmpty(t, listed, "the capture lists no models; is it a /codex/models body?")
	return listed
}

// codexCatalogIDs maps each codex api_model in models.yaml to its catalog id.
func codexCatalogIDs() map[string]models.ModelID {
	ids := map[string]models.ModelID{}
	for _, def := range models.MustGetRegistry().ListModelsByProvider(string(Family)) {
		for _, p := range def.Providers {
			if p.Driver == string(Family) {
				ids[p.APIModel] = models.ModelID(def.ID)
			}
		}
	}
	return ids
}

// Every model Codex offers its users (visibility "list") is reachable through
// the codex driver. Without a mapping, parseCodexModels drops the slug and the
// picker never shows it.
func TestEveryListedCodexModelHasACodexMapping(t *testing.T) {
	mapped := codexCatalogIDs()
	for _, m := range listedCodexModels(t) {
		_, ok := mapped[m.Slug]
		assert.Truef(t, ok,
			"Codex lists %q (visibility \"list\") but models.yaml has no `driver: codex` provider with api_model %q, "+
				"so a codex user never sees it. Add the mapping, its envelope (envelope_gpt56.go) and SupportedModels.",
			m.Slug, m.Slug)
	}
}

// What the model picker shows a codex user: GetAvailableModels applies the
// account's /codex/models report to the catalog's codex models, and ListModels
// hides every entry it disables. Every model Codex lists must come out enabled.
func TestCodexPickerOffersEveryListedModel(t *testing.T) {
	infos := registry.ApplyAvailability(models.MustGetRegistry().ModelsForDriver(string(Family)), recordedReport(t))
	enabled := map[string]bool{}
	for _, mi := range infos {
		if mi.Enabled {
			enabled[mi.APIModel] = true
		}
	}
	for _, m := range listedCodexModels(t) {
		assert.Truef(t, enabled[m.Slug], "Codex lists %q but the codex picker does not offer it", m.Slug)
	}
}

// The codex driver serves exactly what models.yaml maps to codex.
func TestSupportedModelsMatchCodexMappings(t *testing.T) {
	var mapped []models.ModelID
	for _, id := range codexCatalogIDs() {
		if def, ok := models.MustGetRegistry().GetDefinition(string(id)); ok && len(def.Capabilities.OutputModalities) > 0 {
			continue // image models: the image path serves them, not this driver's chat roster
		}
		mapped = append(mapped, id)
	}
	assert.ElementsMatch(t, mapped, SupportedModels)
}

// The backend gates each model on the client version the request claims
// (minimal_client_version), so a CodexVersion below a listed model's minimum
// hides that model from /codex/models — and the authoritative report then
// disables it — even with a correct mapping.
func TestCodexVersionServesEveryListedModel(t *testing.T) {
	for _, m := range listedCodexModels(t) {
		if m.MinimalClientVersion == "" {
			continue
		}
		assert.Truef(t, versionAtLeast(t, CodexVersion, m.MinimalClientVersion),
			"%s needs codex-tui %s; CodexVersion is %s", m.Slug, m.MinimalClientVersion, CodexVersion)
	}
}

// use_responses_lite is what moves Codex's instructions and tools into `input`
// (openai/codex codex-rs/core/tests/suite/responses_lite.rs: no top-level
// `instructions` or `tools`, input[0] is `additional_tools`). The driver's
// envelope choice must agree with it for every mapped model.
func TestCodexEnvelopeFollowsResponsesLite(t *testing.T) {
	mapped := codexCatalogIDs()
	for _, m := range listedCodexModels(t) {
		id, ok := mapped[m.Slug]
		if !ok {
			continue // TestEveryListedCodexModelHasACodexMapping reports it
		}
		assert.Equalf(t, m.UseResponsesLite, usesAdditionalToolsEnvelope(id),
			"%s: use_responses_lite=%v, but the driver sends the %s envelope", m.Slug, m.UseResponsesLite, envelopeName(id))
	}
}

// When /codex/models is unreachable the catalog alone sets the codex prompt
// ceiling (window − max output). It must never sit below what Codex advertises
// (max_context_window): that is how gpt-5.6-* compacted at 231,200 while the
// backend accepted 698,604-token prompts. The live advertised limit can only
// lower the ceiling, so a catalog ceiling above it is safe.
func TestCodexCatalogCeilingCoversAdvertisedLimit(t *testing.T) {
	body, err := os.ReadFile("testdata/codex_models.json")
	require.NoError(t, err)
	var parsed struct {
		Models []struct {
			Slug             string `json:"slug"`
			Visibility       string `json:"visibility"`
			MaxContextWindow int    `json:"max_context_window"`
		} `json:"models"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed))

	mapped := codexCatalogIDs()
	for _, m := range parsed.Models {
		id, ok := mapped[m.Slug]
		if !ok || m.Visibility != "list" {
			continue
		}
		def, ok := models.MustGetRegistry().GetDefinition(string(id))
		require.True(t, ok)
		ceiling := models.PromptCeiling(models.EffectiveContextWindow(def, string(Family)), models.EffectiveMaxOutputTokens(def, string(Family)))
		assert.GreaterOrEqualf(t, ceiling, m.MaxContextWindow,
			"%s@codex: the catalog ceiling %d is below the %d /codex/models advertises", id, ceiling, m.MaxContextWindow)
	}
}

// versionAtLeast compares dotted numeric versions ("0.155.0").
func versionAtLeast(t *testing.T, have, need string) bool {
	t.Helper()
	parse := func(v string) []int {
		var out []int
		for _, part := range strings.Split(v, ".") {
			n, err := strconv.Atoi(part)
			require.NoErrorf(t, err, "version %q is not dotted numeric", v)
			out = append(out, n)
		}
		return out
	}
	h, n := parse(have), parse(need)
	for i := 0; i < len(h) || i < len(n); i++ {
		var hv, nv int
		if i < len(h) {
			hv = h[i]
		}
		if i < len(n) {
			nv = n[i]
		}
		if hv != nv {
			return hv > nv
		}
	}
	return true
}
