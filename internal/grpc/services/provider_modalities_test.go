// Copyright (c) 2025 Reliant Labs
package services

import (
	"slices"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// withRegistry installs a temporary global model registry parsed from the given
// YAML and restores the previous one.
//
// The registry is process-global, so restoring matters: a leaked fixture would
// corrupt every later test in this package. YAML is the input (rather than
// hand-built structs) because it is the same path production uses, so a fixture
// that parses proves the field is wired through unmarshalling too.
func withRegistry(t *testing.T, yaml string) {
	t.Helper()

	prev, err := models.GetRegistry()
	if err != nil {
		t.Fatalf("capture existing registry: %v", err)
	}
	t.Cleanup(func() { models.SetGlobalRegistry(prev) })

	reg, err := models.ParseRegistryFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("parse fixture registry: %v", err)
	}
	models.SetGlobalRegistry(reg)
}

// TestProviderOutputModalities_TextByDefault pins that a provider serving only
// ordinary chat models reports text — the behavior every provider has today.
func TestProviderOutputModalities_TextByDefault(t *testing.T) {
	// No output_modalities declared — exercises the text default.
	withRegistry(t, `
models:
  - id: chat-model
    capabilities:
      supports_tools: true
    providers:
      - driver: openai
        api_model: chat-model
`)

	got := providerOutputModalities("openai")
	if !slices.Equal(got, []string{"text"}) {
		t.Errorf("got %v, want [text]", got)
	}
}

// TestProviderOutputModalities_ImageCapable is the case the field exists for:
// a provider that can generate images must say so, and one that cannot must
// not. Without this distinction the Settings UI cannot tell a user WHY image
// generation is unavailable while their providers look configured.
func TestProviderOutputModalities_ImageCapable(t *testing.T) {
	withRegistry(t, `
models:
  - id: chat-model
    capabilities:
      supports_tools: true
    providers:
      - driver: openai
        api_model: chat
  - id: image-model
    capabilities:
      output_modalities: [image]
    providers:
      - driver: openai
        api_model: img
  - id: text-only-elsewhere
    capabilities:
      supports_tools: true
    providers:
      - driver: anthropic
        api_model: chat
`)

	// openai serves both, so it reports both — text first, extras after.
	got := providerOutputModalities("openai")
	if !slices.Equal(got, []string{"text", "image"}) {
		t.Errorf("openai: got %v, want [text image]", got)
	}

	// anthropic serves no image model, so it must NOT claim image capability.
	// This is the assertion that makes the UI distinction possible.
	got = providerOutputModalities("anthropic")
	if slices.Contains(got, "image") {
		t.Errorf("anthropic: got %v, must not claim image capability", got)
	}
}

// TestProviderOutputModalities_UnknownProvider guards the empty case: a
// provider with no models in the registry claims nothing, rather than
// defaulting to text and implying a capability it cannot serve.
func TestProviderOutputModalities_UnknownProvider(t *testing.T) {
	withRegistry(t, `
models:
  - id: chat-model
    capabilities:
      supports_tools: true
    providers:
      - driver: openai
        api_model: chat
`)

	if got := providerOutputModalities("nonexistent"); len(got) != 0 {
		t.Errorf("got %v, want empty for a provider with no models", got)
	}
}
