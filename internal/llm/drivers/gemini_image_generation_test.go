// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/imagegen"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The model list itself is geminiImageModelIDs, declared in
// gemini_image_models_test.go — one list so a fourth model cannot be added to
// the registry and escape either file's coverage.

// TestGeminiImageModels_MapToBothNativeAndManaged pins both halves of what the
// mapping is for: a BYO AI Studio key can now reach these models, and managed
// credit still can. The managed half matters because it is the live-verified
// path — dropping it while adding the native one would trade a working route
// for an unproven one.
func TestGeminiImageModels_MapToBothNativeAndManaged(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range geminiImageModelIDs {
		definition, ok := registry.GetDefinition(modelID)
		if !ok {
			t.Errorf("model %s is not in the registry", modelID)
			continue
		}

		drivers := map[string]string{}
		for _, provider := range definition.Providers {
			drivers[provider.Driver] = provider.APIModel
		}

		if apiModel, ok := drivers["gemini"]; !ok {
			t.Errorf("model %s has no gemini mapping; a BYO AI Studio key cannot reach it", modelID)
		} else if apiModel != modelID {
			t.Errorf("model %s maps to gemini api_model %q; it must be the AI Studio model name used in the request path", modelID, apiModel)
		}

		if _, ok := drivers["reliant"]; !ok {
			t.Errorf("model %s lost its reliant mapping; managed credits must keep working", modelID)
		}
	}
}

// TestGeminiImageModels_AreServedByTheNativeClient is the guard against the
// specific harm the models.yaml comment warns about.
//
// `gemini` is a priority-1 native driver, so a mapping makes it OUTRANK
// `reliant` for any user holding an AI Studio key. If the resolved config does
// not produce a client that can actually serve the call, the model resolves
// cleanly and then fails at CALL time — after the user has been told which
// model they are getting. This asserts the mapping and the client agree.
func TestGeminiImageModels_AreServedByTheNativeClient(t *testing.T) {
	registry := models.MustGetRegistry()

	for _, modelID := range geminiImageModelIDs {
		// A user holding ONLY a Gemini key must resolve to the gemini driver.
		resolved, err := registry.Resolve(models.ModelSelector{
			ID:                    modelID,
			RequireOutputModality: models.ModalityImage,
		}, []string{"gemini"})
		if err != nil {
			t.Errorf("resolve %s for a gemini-only user: %v", modelID, err)
			continue
		}
		if resolved.Provider.Driver != "gemini" {
			t.Errorf("model %s resolved to %q for a gemini-only user", modelID, resolved.Provider.Driver)
			continue
		}

		config, err := imageGenConfig(resolved, models.DriverConfig{
			DriverID: models.DriverID("gemini"),
			APIKey:   "AIzaSyTestKey",
			Enabled:  true,
		})
		if err != nil {
			t.Errorf("imageGenConfig for %s: %v", modelID, err)
			continue
		}
		if !strings.Contains(config.BaseURL, "generativelanguage.googleapis.com") {
			t.Errorf("model %s got base URL %q, want AI Studio", modelID, config.BaseURL)
		}

		// The decisive assertion: the config must build a working client, and
		// it must be the NATIVE one. The OpenAI-shaped client would post to
		// /images/generations, which AI Studio does not serve.
		client, err := newImageGenClient(config)
		if err != nil {
			t.Errorf("model %s maps to gemini but no client could be built: %v", modelID, err)
			continue
		}
		if _, native := client.(*imagegen.GeminiClient); !native {
			t.Errorf("model %s resolved to a %T; the gemini driver must use the native client", modelID, client)
		}
	}
}

// TestGeminiImageGeneration_PrefersBYOKeyOverManagedCredit pins the
// consequence of the priority-1 mapping, so the ranking is asserted rather
// than assumed: with both credentials, the user's own key wins and managed
// credit is the fallback.
func TestGeminiImageGeneration_PrefersBYOKeyOverManagedCredit(t *testing.T) {
	registry := models.MustGetRegistry()

	selector := models.ModelSelector{
		ID:                    "gemini-3.1-flash-image",
		RequireOutputModality: models.ModalityImage,
	}

	resolved, err := registry.Resolve(selector, []string{"gemini", "reliant"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Provider.Driver != "gemini" {
		t.Errorf("provider = %q, want gemini — a connected BYO key is preferred over managed credit", resolved.Provider.Driver)
	}

	resolved, err = registry.Resolve(selector, []string{"reliant"})
	if err != nil {
		t.Fatalf("resolve (managed only): %v", err)
	}
	if resolved.Provider.Driver != "reliant" {
		t.Errorf("provider = %q, want reliant when no BYO key is configured", resolved.Provider.Driver)
	}
}

// TestImageGenClient_NonGeminiDriversKeepTheOpenAIShapedClient is the other
// side of the branch. openai, codex and the managed reliant proxy all speak
// the OpenAI images format — including for these same Gemini models, which
// LiteLLM translates — so the branch is on the DRIVER, not the model id.
func TestImageGenClient_NonGeminiDriversKeepTheOpenAIShapedClient(t *testing.T) {
	for _, driver := range []string{"openai", "codex", "reliant"} {
		client, err := newImageGenClient(imagegen.Config{
			BaseURL:  "https://example.test/v1",
			APIKey:   "sk-test",
			ModelID:  "gemini-3.1-flash-image",
			APIModel: "gemini-3.1-flash-image",
			Driver:   driver,
		})
		if err != nil {
			t.Errorf("newImageGenClient(%s): %v", driver, err)
			continue
		}
		if _, native := client.(*imagegen.GeminiClient); native {
			t.Errorf("driver %s must keep the OpenAI-shaped client; managed Gemini traffic is translated by LiteLLM", driver)
		}
	}
}
