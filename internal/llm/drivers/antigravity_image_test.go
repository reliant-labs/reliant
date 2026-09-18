// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/drivers/agywire"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/imagegen"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// antigravityImageModelID is the model Antigravity serves on
// v1internal:generateContent, from the recorded capture.
const antigravityImageModelID = "gemini-3.1-flash-image"

// TestAntigravityImageModel_IsMappedForImageOutput pins the registry
// contribution. The tag is what a default image request selects by and the
// modality is the hard filter that keeps text models off the image endpoint; a
// provider mapping without both resolves cleanly and then fails at call time.
func TestAntigravityImageModel_IsMappedForImageOutput(t *testing.T) {
	registry := models.MustGetRegistry()

	definition, ok := registry.GetDefinition(antigravityImageModelID)
	if !ok {
		t.Fatalf("model %q is not defined in models.yaml", antigravityImageModelID)
	}
	if !definition.Capabilities.CanOutput(models.ModalityImage) {
		t.Errorf("model %s does not declare output_modalities: [image]", antigravityImageModelID)
	}

	var mapping *models.ProviderMapping
	for i := range definition.Providers {
		if definition.Providers[i].Driver == antigravity.DriverID {
			mapping = &definition.Providers[i]
		}
	}
	if mapping == nil {
		t.Fatalf("model %s has no antigravity provider; the image tool cannot route there",
			antigravityImageModelID)
	}
	// No effort suffix. Antigravity addresses reasoning effort as a model-id
	// suffix on the CHAT surface; the captured image request sends the bare id,
	// so a suffix here would name a model that does not exist.
	if mapping.APIModel != antigravityImageModelID {
		t.Errorf("api_model = %q, want %q sent verbatim with no effort suffix",
			mapping.APIModel, antigravityImageModelID)
	}
}

// TestAntigravityImageGenConfig_UsesTheGenerateContentEndpoint pins the base
// URL. The chat surface is :streamGenerateContent?alt=sse and the image surface
// is :generateContent with no alt — sending an image request to the chat URL
// asks for a stream this client does not read.
func TestAntigravityImageGenConfig_UsesTheGenerateContentEndpoint(t *testing.T) {
	config, err := imageGenConfig(
		&models.ResolvedModel{
			Definition: models.ModelDefinition{ID: antigravityImageModelID},
			Provider:   models.ProviderMapping{Driver: antigravity.DriverID, APIModel: antigravityImageModelID},
		},
		models.DriverConfig{
			DriverID: models.DriverID(antigravity.DriverID),
			APIKey:   "ya29.synthetic-test-token",
			Enabled:  true,
		},
	)
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}

	if config.BaseURL != agywire.GenerateEndpointURL {
		t.Errorf("BaseURL = %q, want %q", config.BaseURL, agywire.GenerateEndpointURL)
	}
	if strings.Contains(config.BaseURL, "alt=sse") || strings.Contains(config.BaseURL, "streamGenerateContent") {
		t.Errorf("BaseURL = %q; that is the CHAT endpoint, which streams", config.BaseURL)
	}
	if config.APIKey != "ya29.synthetic-test-token" {
		t.Errorf("APIKey = %q; the OAuth bearer must be sent unchanged", config.APIKey)
	}
}

// TestAntigravityImageGenConfig_RewritesAChatBaseURL covers the case that makes
// this provider special: one DriverConfig serves two endpoints, and its
// BaseURL, when a user sets one, is the chat url. Honoring it verbatim would
// send an image request to a streaming endpoint, so a configured base is
// respected only as a HOST override.
func TestAntigravityImageGenConfig_RewritesAChatBaseURL(t *testing.T) {
	config, err := imageGenConfig(
		&models.ResolvedModel{
			Definition: models.ModelDefinition{ID: antigravityImageModelID},
			Provider:   models.ProviderMapping{Driver: antigravity.DriverID, APIModel: antigravityImageModelID},
		},
		models.DriverConfig{
			DriverID: models.DriverID(antigravity.DriverID),
			APIKey:   "ya29.synthetic-test-token",
			BaseURL:  "https://proxy.test/v1internal:streamGenerateContent?alt=sse",
			Enabled:  true,
		},
	)
	if err != nil {
		t.Fatalf("imageGenConfig: %v", err)
	}
	if config.BaseURL != "https://proxy.test/v1internal:generateContent" {
		t.Errorf("BaseURL = %q; the configured HOST must be kept and the image path re-derived", config.BaseURL)
	}
}

// TestNewImageGenClient_AntigravityGetsItsOwnClient pins the dispatch. Falling
// through to the OpenAI-shaped client would POST /images/generations to an
// endpoint that does not serve it; falling through to the genai client would
// decode the double envelope into an empty struct and report success with no
// image.
func TestNewImageGenClient_AntigravityGetsItsOwnClient(t *testing.T) {
	client, err := newImageGenClient(imagegen.Config{
		Driver:   antigravity.DriverID,
		APIKey:   "ya29.synthetic-test-token",
		BaseURL:  agywire.GenerateEndpointURL,
		ModelID:  antigravityImageModelID,
		APIModel: antigravityImageModelID,
	})
	if err != nil {
		t.Fatalf("newImageGenClient: %v", err)
	}
	if _, ok := client.(*imagegen.AntigravityClient); !ok {
		t.Fatalf("client = %T, want *imagegen.AntigravityClient", client)
	}
}

// TestAntigravityImageModel_ResolvesUnderImageModality walks the real resolver
// with only an Antigravity credential configured, which is the situation a user
// who signed in with Antigravity and nothing else is in.
func TestAntigravityImageModel_ResolvesUnderImageModality(t *testing.T) {
	registry := models.MustGetRegistry()

	resolved, err := registry.Resolve(models.ModelSelector{
		Tags:                  []string{DefaultImageGenTag},
		RequireOutputModality: models.ModalityImage,
	}, []string{antigravity.DriverID})
	if err != nil {
		t.Fatalf("a default image request with only antigravity configured must resolve: %v", err)
	}
	if resolved.Provider.Driver != antigravity.DriverID {
		t.Errorf("resolved provider = %q, want antigravity", resolved.Provider.Driver)
	}
}
