// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// openAIImageModelIDs are the OpenAI image-generation entries this file guards.
// Named once so a future addition fails in one place rather than silently
// escaping coverage.
//
// These replaced the gpt-image-1 family. Per LiteLLM's model catalog, gpt-image-1
// deprecates 2026-10-23 and gpt-image-1-mini 2026-12-01; the entries here carry
// no deprecation date at all.
var openAIImageModelIDs = []string{
	"gpt-image-2.5-flare",
	"gpt-image-2.5-sunburst",
	"gpt-image-2",
}

// retiredOpenAIImageModelIDs are ids that must never appear in the registry
// again. Every one of them is past shutdown or dated to shut down.
var retiredOpenAIImageModelIDs = []string{
	"gpt-image-1",          // deprecates 2026-10-23
	"gpt-image-1-mini",     // deprecates 2026-12-01
	"gpt-image-1.5",        // deprecates 2026-12-01
	"dall-e-2",             // deprecated 2026-05-12
	"dall-e-3",             // deprecated 2026-05-12
	"chatgpt-image-latest", // deprecates 2026-12-01
}

// TestOpenAIImageModels_DeclareImageOutputAndImageGenTag pins the registry
// shape the image path depends on. The tag is what a default request selects
// by, and the modality is the hard filter that keeps a text model off the image
// endpoint; a model carrying one without the other resolves fine and then fails
// at call time.
func TestOpenAIImageModels_DeclareImageOutputAndImageGenTag(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range openAIImageModelIDs {
		definition, ok := registry.GetDefinition(modelID)
		if !ok {
			t.Errorf("model %q is not defined in models.yaml", modelID)
			continue
		}

		if !definition.Capabilities.CanOutput(models.ModalityImage) {
			t.Errorf("model %s produces %v; an image-generation model must declare output_modalities: [image]",
				modelID, definition.Capabilities.EffectiveOutputModalities())
		}
		if definition.Capabilities.CanOutput(models.ModalityText) {
			t.Errorf("model %s claims text output; it is served on the images endpoint and would be offered in the chat picker",
				modelID)
		}

		var hasImageGenTag bool
		for _, tag := range definition.Tags {
			if tag == DefaultImageGenTag {
				hasImageGenTag = true
			}
		}
		if !hasImageGenTag {
			t.Errorf("model %s does not carry the %q tag, so a default image request cannot select it",
				modelID, DefaultImageGenTag)
		}

		// Billed per image token, not per 1M text tokens. A value in the
		// per-token fields is read by the model picker and shown to a user as
		// fact.
		if definition.Cost.InputPer1M != 0 || definition.Cost.OutputPer1M != 0 {
			t.Errorf("model %s declares per-token cost (%v in / %v out); image models are billed per image",
				modelID, definition.Cost.InputPer1M, definition.Cost.OutputPer1M)
		}
	}
}

// TestRetiredOpenAIImageModelsAreGone pins the removal itself, and is the
// lesson from having twice shipped an image model that was already dead or
// dying. A registry entry for a retired model is strictly worse than no entry:
// it resolves cleanly, wins provider selection, and then 404s at call time.
//
// The api_model sweep is by PREFIX, not by exact id, because the whole
// gpt-image-1 family shares a shutdown horizon (1, 1-mini and 1.5), and
// dall-e-2/3 are already past theirs. Re-adding any of them should fail here
// rather than in production.
func TestRetiredOpenAIImageModelsAreGone(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range retiredOpenAIImageModelIDs {
		if _, ok := registry.GetDefinition(modelID); ok {
			t.Errorf("retired model %s is still defined in models.yaml; OpenAI has dated it for shutdown, so calls will 404", modelID)
		}
	}

	retiredAPIModelPrefixes := []string{"gpt-image-1", "dall-e"}
	for _, definition := range registry.GetModelsByTag(DefaultImageGenTag) {
		for _, provider := range definition.Providers {
			for _, prefix := range retiredAPIModelPrefixes {
				if strings.HasPrefix(provider.APIModel, prefix) {
					t.Errorf("model %s maps to retired api_model %q (family %q is dated for shutdown)",
						definition.ID, provider.APIModel, prefix)
				}
			}
		}
	}
}

// TestGPTImage25Models_MapToAllThreeSurfaces pins that the 2.5 family is
// reachable on every surface that was VERIFIED to serve it.
//
// This test previously asserted the opposite — that a `codex` mapping must NOT
// exist — on the reasoning that only gpt-image-2 had been confirmed on the
// ChatGPT backend and a speculative mapping would outrank reliant and then fail
// at call time. That reasoning was sound; its premise was simply wrong. A live
// probe against chatgpt.com returned HTTP 200 for both 2.5 ids, and a full
// generation with gpt-image-2.5-flare returned a real 753,964-byte PNG. So the
// mapping is no longer speculative, and withholding it would push a
// subscription holder onto metered credits for a model their plan covers.
//
// The general rule the old comment expressed still stands: add a provider
// mapping only once that surface is known to serve the model.
func TestGPTImage25Models_MapToAllThreeSurfaces(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst"} {
		definition, ok := registry.GetDefinition(modelID)
		if !ok {
			t.Errorf("model %q is not defined in models.yaml", modelID)
			continue
		}

		drivers := map[string]string{}
		for _, provider := range definition.Providers {
			drivers[provider.Driver] = provider.APIModel
		}

		if apiModel, ok := drivers["openai"]; !ok {
			t.Errorf("model %s has no `openai` mapping; the platform API serves it and a BYO sk- key must reach it", modelID)
		} else if apiModel != modelID {
			t.Errorf("model %s openai api_model = %q, want %q", modelID, apiModel, modelID)
		}

		if apiModel, ok := drivers["reliant"]; !ok {
			t.Errorf("model %s has no `reliant` mapping; managed credit cannot reach it", modelID)
		} else if apiModel != modelID {
			t.Errorf("model %s reliant api_model = %q, want %q; LiteLLM routes purely by model_name", modelID, apiModel, modelID)
		}

		if apiModel, ok := drivers["codex"]; !ok {
			t.Errorf("model %s has no `codex` mapping; the ChatGPT backend was VERIFIED to serve it "+
				"with a live 200, so a subscription holder should not be pushed onto credits", modelID)
		} else if apiModel != modelID {
			t.Errorf("model %s codex api_model = %q, want %q", modelID, apiModel, modelID)
		}

		// Order is the assertion, not an accident: findBestProvider breaks a
		// priority tie by YAML order, and codex/openai are both priority 1. A
		// subscription-covered surface should be preferred over one that bills
		// a platform key, and both over managed credits.
		if got := definition.Providers[0].Driver; got != "codex" {
			t.Errorf("model %s first provider = %q, want codex to win the priority tie", modelID, got)
		}
	}
}

// TestGPTImage2_ServedByAllThreeSurfaces is the one-id-three-providers case.
// gpt-image-2 is genuinely the same model on the ChatGPT backend (OAuth bearer
// plus chatgpt-account-id), on the OpenAI platform API (static sk- key), and
// through the managed LiteLLM proxy. It gets ONE registry id with three
// mappings rather than three ids, because the api_model string is identical on
// every surface and findBestProvider already exists to pick between credentials
// the user actually holds.
func TestGPTImage2_ServedByAllThreeSurfaces(t *testing.T) {
	registry := models.MustGetRegistry()

	definition, ok := registry.GetDefinition("gpt-image-2")
	if !ok {
		t.Fatalf("model gpt-image-2 is not defined in models.yaml")
	}

	drivers := map[string]string{}
	for _, provider := range definition.Providers {
		drivers[provider.Driver] = provider.APIModel
	}

	for _, driver := range []string{"codex", "openai", "reliant"} {
		apiModel, ok := drivers[driver]
		if !ok {
			t.Errorf("model gpt-image-2 has no %q mapping", driver)
			continue
		}
		if apiModel != "gpt-image-2" {
			t.Errorf("model gpt-image-2 %s api_model = %q, want gpt-image-2", driver, apiModel)
		}
	}
}

// TestDefaultImageRequest_ManagedUserGetsFlare pins the DEFAULT, which is
// decided by YAML order: tag scores tie across every image-gen model, so the
// earliest definition with an available provider wins. Flare is OpenAI's stated
// default for most applications — higher quality than gpt-image-2 at roughly
// half the latency — so it must be the first image entry in models.yaml.
func TestDefaultImageRequest_ManagedUserGetsFlare(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag},
		RequireOutputModality: models.ModalityImage,
	}
	resolved, err := registry.Resolve(selector, []string{"reliant"})
	if err != nil {
		t.Fatalf("resolve with only managed credit configured: %v", err)
	}
	if resolved.Definition.ID != "gpt-image-2.5-flare" {
		t.Errorf("default managed image model = %q, want gpt-image-2.5-flare", resolved.Definition.ID)
	}
	if resolved.Provider.Driver != "reliant" {
		t.Errorf("provider = %q, want reliant", resolved.Provider.Driver)
	}
}

// TestDefaultImageRequest_OpenAIKeyUserGetsFlare is the BYO half of the same
// default. A user with only a platform sk- key must reach the 2.5 tier
// directly, not fall through to gpt-image-2.
func TestDefaultImageRequest_OpenAIKeyUserGetsFlare(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag},
		RequireOutputModality: models.ModalityImage,
	}
	resolved, err := registry.Resolve(selector, []string{"openai"})
	if err != nil {
		t.Fatalf("resolve with only an OpenAI platform key: %v", err)
	}
	if resolved.Definition.ID != "gpt-image-2.5-flare" {
		t.Errorf("default OpenAI image model = %q, want gpt-image-2.5-flare", resolved.Definition.ID)
	}
	if resolved.Provider.Driver != "openai" {
		t.Errorf("provider = %q, want openai", resolved.Provider.Driver)
	}
}

// TestFlagshipImageRequest_GetsSunburst covers the "higher powered model"
// request. Sunburst is the precision/detail tier, and `flagship` is the
// registry's existing vocabulary for a family's top model, so [image-gen,
// flagship] must land there rather than degrading onto the everyday default.
func TestFlagshipImageRequest_GetsSunburst(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag, "flagship"},
		RequireOutputModality: models.ModalityImage,
	}
	resolved, err := registry.Resolve(selector, []string{"reliant"})
	if err != nil {
		t.Fatalf("resolve [image-gen, flagship]: %v", err)
	}
	if resolved.Definition.ID != "gpt-image-2.5-sunburst" {
		t.Errorf("flagship image model = %q, want gpt-image-2.5-sunburst", resolved.Definition.ID)
	}
}
