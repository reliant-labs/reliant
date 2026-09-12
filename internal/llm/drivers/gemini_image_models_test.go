// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// geminiImageModelIDs are the Google Gemini image-generation entries this file
// guards. Named once so a future addition fails in one place rather than
// silently escaping coverage.
//
// These replaced the Imagen 4 family, which Google retired (Vertex endpoints
// shut down 2026-06-30; the Gemini-API surface lists 2026-08-17). Requests to
// the old publishers/google/models/imagen-4.0-*:predict endpoints 404.
var geminiImageModelIDs = []string{
	"gemini-3.1-flash-lite-image",
	"gemini-3.1-flash-image",
	"gemini-3-pro-image",
}

// TestGeminiImageModels_DeclareImageOutputAndImageGenTag pins the registry
// shape the image path depends on. The tag is what a default request selects
// by, and the modality is the hard filter that keeps a text model off the image
// endpoint; a model carrying one without the other resolves fine and then fails
// at call time, which is the exact failure this file exists to prevent.
func TestGeminiImageModels_DeclareImageOutputAndImageGenTag(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range geminiImageModelIDs {
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

		// Gemini image models are billed per image (0.0336 / 0.0672 / 0.134
		// for flash-lite / flash / pro). The per-1M-token fields cannot
		// express that, and a value there would be read by the picker and
		// shown to a user as fact.
		if definition.Cost.InputPer1M != 0 || definition.Cost.OutputPer1M != 0 {
			t.Errorf("model %s declares per-token cost (%v in / %v out); image models are billed per image",
				modelID, definition.Cost.InputPer1M, definition.Cost.OutputPer1M)
		}
	}
}

// TestGeminiImageModels_RouteOnlyThroughServeableProviders is the load-bearing
// assertion of this family's registry shape: every mapped provider must be one
// something can actually CALL.
//
// This test previously required managed-only routing, on the grounds that
// Vertex's :generateContent schema is not the OpenAI images schema the imagegen
// client speaks and only LiteLLM performs that translation. The `gemini` half
// of that is no longer true — imagegen.GeminiClient calls AI Studio's
// :generateContent natively — so `gemini` joined `reliant` as a serveable
// route. The underlying RULE did not change, only which drivers satisfy it,
// which is why the allow-list is explicit and small.
//
// `vertexai` is still excluded, and the reasoning is the original one: no
// client speaks its schema with service-account credentials. A mapping would be
// actively harmful rather than merely unused, because vertexai is a priority-1
// native driver — it would WIN provider selection over reliant for any user
// with Vertex credentials and then fail at call time.
func TestGeminiImageModels_RouteOnlyThroughServeableProviders(t *testing.T) {
	registry := models.MustGetRegistry()

	// Serveable == a client exists that speaks this provider's image API.
	//   reliant — LiteLLM translates to the OpenAI images shape.
	//   gemini  — imagegen.GeminiClient calls AI Studio natively.
	serveable := map[string]bool{"reliant": true, "gemini": true}

	for _, modelID := range geminiImageModelIDs {
		definition, ok := registry.GetDefinition(modelID)
		if !ok {
			t.Errorf("model %q is not defined in models.yaml", modelID)
			continue
		}

		if len(definition.Providers) == 0 {
			t.Errorf("model %s has no providers and can never resolve", modelID)
			continue
		}
		sawManaged := false
		for _, provider := range definition.Providers {
			if !serveable[provider.Driver] {
				t.Errorf("model %s maps to provider %q, which no image client can call; it would win selection and then fail at call time",
					modelID, provider.Driver)
			}
			if models.IsManagedDriver(models.DriverID(provider.Driver)) {
				sawManaged = true
			}
			if provider.APIModel == "" {
				t.Errorf("model %s provider %s has no api_model", modelID, provider.Driver)
			}
		}
		// Managed credit is the fallback for a user with no Google key of
		// their own, and it is the live-verified path. It must not be dropped.
		if !sawManaged {
			t.Errorf("model %s lost its managed provider; managed credits could no longer generate it", modelID)
		}
	}
}

// TestGeminiImageModels_APIModelMatchesVertexPublisherID pins the provider-side
// identifiers. LiteLLM builds the Vertex URL by interpolating this string
// directly into publishers/google/models/<model>:generateContent, so a wrong or
// abbreviated id is a 404 at call time rather than a resolution error. The same
// string must be the `model_name` in control-plane's deploy/litellm/config.yaml,
// because LiteLLM routes purely by model_name.
//
// Source: LiteLLM's model_prices_and_context_window.json, the vertex_ai entries
// with mode: image_generation.
func TestGeminiImageModels_APIModelMatchesVertexPublisherID(t *testing.T) {
	registry := models.MustGetRegistry()

	wantAPIModel := map[string]string{
		"gemini-3.1-flash-lite-image": "gemini-3.1-flash-lite-image",
		"gemini-3.1-flash-image":      "gemini-3.1-flash-image",
		"gemini-3-pro-image":          "gemini-3-pro-image",
	}

	for modelID, want := range wantAPIModel {
		definition, ok := registry.GetDefinition(modelID)
		if !ok {
			t.Errorf("model %q is not defined in models.yaml", modelID)
			continue
		}
		for _, provider := range definition.Providers {
			if provider.APIModel != want {
				t.Errorf("model %s provider %s api_model = %q, want %q (the Vertex publisher model id)",
					modelID, provider.Driver, provider.APIModel, want)
			}
		}
	}
}

// TestGeminiImageModels_ResolveUnderImageModality walks the real resolver with
// only managed credit configured, which is the situation the reliant-only
// mapping is for.
func TestGeminiImageModels_ResolveUnderImageModality(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range geminiImageModelIDs {
		selector := models.ModelSelector{
			ID:                    modelID,
			RequireOutputModality: models.ModalityImage,
		}
		resolved, err := registry.Resolve(selector, []string{"reliant"})
		if err != nil {
			t.Errorf("resolve %s with managed credit: %v", modelID, err)
			continue
		}
		if resolved.Provider.Driver != "reliant" {
			t.Errorf("model %s resolved to provider %q, want reliant", modelID, resolved.Provider.Driver)
		}
	}
}

// TestGeminiImageModels_UnreachableByTextRequest is the inverse guarantee: an
// image model must never satisfy a chat request. Without the modality filter
// these would be reachable, and a chat turn would be sent to an image endpoint.
//
// This matters more for the Gemini image family than it did for Imagen: these
// ids share the `gemini-3.1` prefix with real chat models, so a prefix-based
// mistake anywhere in selection would land here first.
func TestGeminiImageModels_UnreachableByTextRequest(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range geminiImageModelIDs {
		selector := models.ModelSelector{
			ID:                    modelID,
			RequireOutputModality: models.ModalityText,
		}
		_, err := registry.Resolve(selector, []string{"reliant", "vertexai", "gemini", "openai"})
		if err == nil {
			t.Errorf("resolving %s for text output succeeded; an image-only model must not serve a chat request", modelID)
			continue
		}
		if !strings.Contains(err.Error(), "cannot generate text") {
			t.Errorf("model %s: error should name the modality mismatch, got: %v", modelID, err)
		}
	}
}

// TestGeminiImageModels_ExcludedFromChatPicker is the user-facing consequence
// of the modality declaration, asserted against the production registry rather
// than a fixture.
func TestGeminiImageModels_ExcludedFromChatPicker(t *testing.T) {
	registry := models.MustGetRegistry()

	visible := make(map[string]bool)
	for _, definition := range registry.GetUserVisibleModels() {
		visible[definition.ID] = true
	}

	for _, modelID := range geminiImageModelIDs {
		if visible[modelID] {
			t.Errorf("model %s appears in the chat model picker; it cannot serve a chat request", modelID)
		}
	}
}

// TestRetiredImagenModelsAreGone pins the removal itself. Google retired the
// Imagen 4 endpoints, so a registry entry for one resolves cleanly and then
// 404s at call time — strictly worse than not offering the model. Re-adding one
// should fail here rather than in production.
func TestRetiredImagenModelsAreGone(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range []string{"imagen-4-fast", "imagen-4", "imagen-4-ultra"} {
		if _, ok := registry.GetDefinition(modelID); ok {
			t.Errorf("retired model %s is still defined in models.yaml; Google shut down the Imagen 4 endpoints, so calls 404", modelID)
		}
	}

	for _, definition := range registry.GetModelsByTag(DefaultImageGenTag) {
		for _, provider := range definition.Providers {
			if strings.Contains(provider.APIModel, "imagen") {
				t.Errorf("model %s maps to retired Imagen api_model %q", definition.ID, provider.APIModel)
			}
		}
	}
}

// TestVertexChatModels_DoNotClaimImageOutput guards the distinction that makes
// this area subtle. The Gemini/Vertex chat models declare supported_file_types
// including "image" — that is what they CONSUME (vision input), not what they
// produce. If one ever gained output_modalities: [image], an image request
// could resolve to a vision chat model with no generation endpoint behind it.
//
// The Gemini image models now share a name prefix with the Gemini CHAT models
// (`gemini-3.1-flash-image` vs `gemini-3.1-flash-lite-preview`), so this guard
// is doing more work than it was for Imagen: the two families are no longer
// distinguishable by name, only by declared modality. The exclusion below is by
// explicit id, never by prefix, for exactly that reason.
func TestVertexChatModels_DoNotClaimImageOutput(t *testing.T) {
	registry := models.MustGetRegistry()

	imageModelSet := make(map[string]bool, len(geminiImageModelIDs))
	for _, modelID := range geminiImageModelIDs {
		imageModelSet[modelID] = true
	}

	for _, definition := range registry.GetModelsByTag(DefaultImageGenTag) {
		if imageModelSet[definition.ID] {
			continue
		}
		for _, provider := range definition.Providers {
			if provider.Driver == "vertexai" || provider.Driver == "gemini" {
				t.Errorf("model %s is tagged %q and maps to %s; a Google chat model consuming images is not an image GENERATOR",
					definition.ID, DefaultImageGenTag, provider.Driver)
			}
		}
	}
}
