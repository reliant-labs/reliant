// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// TestImageGenModelsAreDeclaredWithImageModality pins the registry contribution
// this phase makes: at least one model that declares output_modalities: [image]
// and carries the image-gen tag, with both a BYO provider and the managed
// reliant provider so credit is a fallback rather than the only option.
func TestImageGenModelsAreDeclaredWithImageModality(t *testing.T) {
	registry := models.MustGetRegistry()

	tagged := registry.GetModelsByTag(DefaultImageGenTag)
	if len(tagged) == 0 {
		t.Fatalf("no models carry the %q tag; image generation cannot resolve a default", DefaultImageGenTag)
	}

	sawManagedProvider := false
	sawBYOProvider := false
	for _, model := range tagged {
		if !model.Capabilities.CanOutput(models.ModalityImage) {
			t.Errorf("model %s is tagged %q but declares output modalities %v",
				model.ID, DefaultImageGenTag, model.Capabilities.EffectiveOutputModalities())
		}
		// Image pricing is per image, not per token, and spend is metered by
		// the control-plane proxy from LiteLLM's cost header. A per-token cost
		// here would be read by the model picker and shown to a user as fact.
		if model.Cost.InputPer1M != 0 || model.Cost.OutputPer1M != 0 {
			t.Errorf("model %s declares per-token cost (%v in / %v out); image models are billed per image",
				model.ID, model.Cost.InputPer1M, model.Cost.OutputPer1M)
		}
		for _, provider := range model.Providers {
			if provider.APIModel == "" {
				t.Errorf("model %s provider %s has no api_model", model.ID, provider.Driver)
			}
			if models.IsManagedDriver(models.DriverID(provider.Driver)) {
				sawManagedProvider = true
			} else {
				sawBYOProvider = true
			}
		}
	}

	if !sawManagedProvider {
		t.Error("no image-gen model maps to the managed reliant provider; managed credits could not generate images")
	}
	if !sawBYOProvider {
		t.Error("no image-gen model maps to a BYO provider; a user's own key could not generate images")
	}
}

// TestImageGenResolution_RejectsTextModels is the modality guarantee at the
// resolution layer: naming a text model explicitly must fail rather than send a
// chat model to an image endpoint.
func TestImageGenResolution_RejectsTextModels(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		ID:                    string(models.Claude5Sonnet),
		RequireOutputModality: models.ModalityImage,
	}
	_, err := registry.Resolve(selector, []string{"anthropic", "openai", "reliant"})
	if err == nil {
		t.Fatal("resolving a text model for image output should fail")
	}
	if !strings.Contains(err.Error(), "cannot generate image") {
		t.Errorf("error should name the modality mismatch, got: %v", err)
	}
}

// TestImageGenResolution_TagDegradationCannotPickTextModel guards the reason
// modality is a hard filter and not just another tag: tag scoring degrades
// gracefully, so [image-gen, cheap] would otherwise settle for a text model
// that matched only "cheap".
func TestImageGenResolution_TagDegradationCannotPickTextModel(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag, models.TagCheap},
		RequireOutputModality: models.ModalityImage,
	}
	resolved, err := registry.Resolve(selector, []string{"anthropic", "openai", "gemini", "reliant"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolved.Definition.Capabilities.CanOutput(models.ModalityImage) {
		t.Fatalf("resolved %s, which produces %v — tag degradation escaped the modality filter",
			resolved.Definition.ID, resolved.Definition.Capabilities.EffectiveOutputModalities())
	}
}

// TestImageGenConfig_ManagedDriverRoutesThroughProxy pins the billing-critical
// path: a managed request must target the control-plane base URL, because that
// proxy is the only place image spend is metered.
func TestImageGenConfig_ManagedDriverRoutesThroughProxy(t *testing.T) {
	t.Setenv("RELIANT_API_BASE_URL", "http://control-plane.test:8090/v1")

	resolved := &models.ResolvedModel{
		Definition: models.ModelDefinition{ID: "gpt-image-2.5-flare"},
		Provider:   models.ProviderMapping{Driver: "reliant", APIModel: "gpt-image-2.5-flare"},
	}
	driverConfig := models.DriverConfig{
		DriverID: models.DriverID("reliant"),
		APIKey:   "rly_managed_token",
		Enabled:  true,
	}

	config, err := imageGenConfig(resolved, driverConfig)
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}
	if config.BaseURL != "http://control-plane.test:8090/v1" {
		t.Errorf("BaseURL = %q; managed image generation must route through the metered proxy", config.BaseURL)
	}
	if config.APIModel != "gpt-image-2.5-flare" || config.Driver != "reliant" {
		t.Errorf("config = %+v", config)
	}
}

// TestImageGenConfig_BYOProviderUsesItsOwnEndpoint is the other half: a user's
// own key goes straight to the provider, not through Reliant's meter.
func TestImageGenConfig_BYOProviderUsesItsOwnEndpoint(t *testing.T) {
	resolved := &models.ResolvedModel{
		Definition: models.ModelDefinition{ID: "gpt-image-2.5-flare"},
		Provider:   models.ProviderMapping{Driver: "openai", APIModel: "gpt-image-2.5-flare"},
	}
	driverConfig := models.DriverConfig{
		DriverID: models.DriverID("openai"),
		APIKey:   "sk-user-key",
		Enabled:  true,
	}

	config, err := imageGenConfig(resolved, driverConfig)
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}
	if config.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("BaseURL = %q, want the OpenAI endpoint", config.BaseURL)
	}
	if config.APIKey != "sk-user-key" {
		t.Errorf("APIKey = %q; a BYO key must be sent unchanged", config.APIKey)
	}
}

// TestImageGenResolution_PrefersBYOOverManagedCredit pins the credential order
// the task requires: with both an OpenAI key and managed credits configured,
// the user's own key wins.
func TestImageGenResolution_PrefersBYOOverManagedCredit(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag},
		RequireOutputModality: models.ModalityImage,
	}
	resolved, err := registry.Resolve(selector, []string{"openai", "reliant"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Provider.Driver != "openai" {
		t.Errorf("provider = %q, want openai — a connected BYO key must be preferred over managed credit",
			resolved.Provider.Driver)
	}

	// With only managed credit configured, it is used.
	resolved, err = registry.Resolve(selector, []string{"reliant"})
	if err != nil {
		t.Fatalf("resolve (managed only): %v", err)
	}
	if resolved.Provider.Driver != "reliant" {
		t.Errorf("provider = %q, want reliant", resolved.Provider.Driver)
	}
}
