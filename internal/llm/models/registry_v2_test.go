// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test YAML fixtures for consistent testing
const testModelsYAML = `
models:
  - id: claude-4-opus
    name: Claude 4 Opus
    tags: [flagship, reasoning]
    visibility: user
    capabilities:
      can_reason: true
      supports_tools: true
      supports_attachments: true
      supports_streaming: true
      supports_caching: true
      max_context_window: 200000
      max_output_tokens: 32000
    cost:
      input_per_1m: 15.0
      output_per_1m: 75.0
      cached_input_per_1m: 1.5
    providers:
      - driver: anthropic
        api_model: claude-opus-4-20250514
      - driver: openrouter
        api_model: anthropic/claude-opus-4

  - id: claude-4-sonnet
    name: Claude 4 Sonnet
    tags: [flagship, moderate, fast]
    visibility: user
    capabilities:
      can_reason: false
      supports_tools: true
      supports_attachments: true
      supports_streaming: true
      supports_caching: true
      max_context_window: 200000
      max_output_tokens: 16000
    cost:
      input_per_1m: 3.0
      output_per_1m: 15.0
    providers:
      - driver: anthropic
        api_model: claude-sonnet-4-20250514
      - driver: openrouter
        api_model: anthropic/claude-sonnet-4

  - id: gpt-4o
    name: GPT-4o
    tags: [flagship, fast]
    visibility: user
    capabilities:
      can_reason: false
      supports_tools: true
      supports_attachments: true
      supports_streaming: true
      supports_caching: false
      max_context_window: 128000
      max_output_tokens: 16384
    cost:
      input_per_1m: 2.5
      output_per_1m: 10.0
    providers:
      - driver: openai
        api_model: gpt-4o
      - driver: openrouter
        api_model: openai/gpt-4o

  - id: gpt-4o-mini
    name: GPT-4o Mini
    tags: [fast, cheap, meta]
    visibility: meta
    capabilities:
      can_reason: false
      supports_tools: true
      supports_attachments: true
      supports_streaming: true
      supports_caching: false
      max_context_window: 128000
      max_output_tokens: 16384
    cost:
      input_per_1m: 0.15
      output_per_1m: 0.60
    providers:
      - driver: openai
        api_model: gpt-4o-mini

  - id: ollama-qwen
    name: Ollama Qwen 2.5
    tags: [local, fast, cheap]
    visibility: dev
    capabilities:
      can_reason: false
      supports_tools: false
      supports_attachments: false
      supports_streaming: true
      supports_caching: false
      max_context_window: 32000
      max_output_tokens: 8000
    cost:
      input_per_1m: 0.0
      output_per_1m: 0.0
    providers:
      - driver: local
        api_model: qwen2.5:32b
`

// createTestRegistry creates a registry from the test YAML
func createTestRegistry(t *testing.T) *ModelRegistry {
	t.Helper()
	reg, err := ParseRegistryFromBytes([]byte(testModelsYAML))
	if err != nil {
		t.Fatalf("failed to parse test YAML: %v", err)
	}
	return reg
}

// =============================================================================
// ParseRegistryFromBytes Tests
// =============================================================================

func TestParseRegistryFromBytes(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantErr   bool
		wantCount int
	}{
		{
			name:      "valid YAML with multiple models",
			yaml:      testModelsYAML,
			wantErr:   false,
			wantCount: 5,
		},
		{
			name:      "empty models list",
			yaml:      "models: []",
			wantErr:   false,
			wantCount: 0,
		},
		{
			name:    "invalid YAML syntax",
			yaml:    "models: [invalid yaml",
			wantErr: true,
		},
		{
			name: "duplicate model IDs",
			yaml: `
models:
  - id: test-model
    name: Test Model
    tags: []
    visibility: user
    providers:
      - driver: openai
        api_model: test
  - id: test-model
    name: Duplicate
    tags: []
    visibility: user
    providers:
      - driver: openai
        api_model: test2
`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := ParseRegistryFromBytes([]byte(tt.yaml))
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(reg.models) != tt.wantCount {
				t.Errorf("got %d models, want %d", len(reg.models), tt.wantCount)
			}
		})
	}
}

// =============================================================================
// GetDefinition Tests
// =============================================================================

func TestGetDefinition(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name     string
		id       string
		wantOk   bool
		wantName string
	}{
		{
			name:     "existing model",
			id:       "claude-4-opus",
			wantOk:   true,
			wantName: "Claude 4 Opus",
		},
		{
			name:     "another existing model",
			id:       "gpt-4o",
			wantOk:   true,
			wantName: "GPT-4o",
		},
		{
			name:   "non-existent model",
			id:     "nonexistent-model",
			wantOk: false,
		},
		{
			name:   "empty ID",
			id:     "",
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, ok := reg.GetDefinition(tt.id)
			if ok != tt.wantOk {
				t.Errorf("GetDefinition(%q) ok = %v, want %v", tt.id, ok, tt.wantOk)
				return
			}
			if ok && model.Name != tt.wantName {
				t.Errorf("GetDefinition(%q).Name = %q, want %q", tt.id, model.Name, tt.wantName)
			}
		})
	}
}

// =============================================================================
// GetModelsByTag Tests
// =============================================================================

func TestGetModelsByTag(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name        string
		tag         string
		wantIDs     []string
		wantOrdered bool // if true, checks exact order
	}{
		{
			name:        "flagship tag - multiple models in order",
			tag:         "flagship",
			wantIDs:     []string{"claude-4-opus", "claude-4-sonnet", "gpt-4o"},
			wantOrdered: true,
		},
		{
			name:        "fast tag",
			tag:         "fast",
			wantIDs:     []string{"claude-4-sonnet", "gpt-4o", "gpt-4o-mini", "ollama-qwen"},
			wantOrdered: true,
		},
		{
			name:        "reasoning tag - single model",
			tag:         "reasoning",
			wantIDs:     []string{"claude-4-opus"},
			wantOrdered: true,
		},
		{
			name:    "nonexistent tag",
			tag:     "nonexistent",
			wantIDs: nil,
		},
		{
			name:        "local tag",
			tag:         "local",
			wantIDs:     []string{"ollama-qwen"},
			wantOrdered: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := reg.GetModelsByTag(tt.tag)

			if len(models) != len(tt.wantIDs) {
				t.Errorf("GetModelsByTag(%q) returned %d models, want %d", tt.tag, len(models), len(tt.wantIDs))
				return
			}

			for i, model := range models {
				if model.ID != tt.wantIDs[i] {
					t.Errorf("GetModelsByTag(%q)[%d].ID = %q, want %q", tt.tag, i, model.ID, tt.wantIDs[i])
				}
			}
		})
	}
}

// =============================================================================
// GetUserVisibleModels Tests (visibility-based filtering)
// =============================================================================

func TestGetUserVisibleModels(t *testing.T) {
	reg := createTestRegistry(t)

	models := reg.GetUserVisibleModels()

	// Should only include models with visibility=user or empty (default)
	expectedIDs := map[string]bool{
		"claude-4-opus":   true,
		"claude-4-sonnet": true,
		"gpt-4o":          true,
	}

	if len(models) != len(expectedIDs) {
		t.Errorf("GetUserVisibleModels() returned %d models, want %d", len(models), len(expectedIDs))
	}

	for _, model := range models {
		if !expectedIDs[model.ID] {
			t.Errorf("GetUserVisibleModels() returned unexpected model: %s", model.ID)
		}
	}
}

// =============================================================================
// findModelsByBestMatch Tests (weighted scoring)
// =============================================================================

func TestFindModelsByBestMatch(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name    string
		tags    []string
		wantIDs []string // Expected order (highest score first)
	}{
		{
			name:    "single tag - returns all matching models",
			tags:    []string{"flagship"},
			wantIDs: []string{"claude-4-opus", "claude-4-sonnet", "gpt-4o"},
		},
		{
			// Perfect match (both tags) scores highest
			// flagship=2, fast=1, both=3
			name:    "two tags - perfect matches first",
			tags:    []string{"flagship", "fast"},
			wantIDs: []string{"claude-4-sonnet", "gpt-4o", "claude-4-opus", "gpt-4o-mini", "ollama-qwen"},
			// claude-4-sonnet: flagship+fast = 3
			// gpt-4o: flagship+fast = 3 (but later in definition order)
			// claude-4-opus: flagship only = 2
			// gpt-4o-mini: fast only = 1
			// ollama-qwen: fast only = 1
		},
		{
			// First tag has higher weight
			// cheap=2, flagship=1
			name:    "tag order matters - cheap first",
			tags:    []string{"cheap", "flagship"},
			wantIDs: []string{"gpt-4o-mini", "ollama-qwen", "claude-4-opus", "claude-4-sonnet", "gpt-4o"},
			// gpt-4o-mini: cheap = 2
			// ollama-qwen: cheap = 2 (but later in definition)
			// claude-4-opus: flagship = 1
			// claude-4-sonnet: flagship = 1
			// gpt-4o: flagship = 1
		},
		{
			// Three tags: local=4, fast=2, cheap=1
			// ollama-qwen has all three = 7
			// gpt-4o-mini has fast+cheap = 3
			name:    "three tags - perfect match wins",
			tags:    []string{"local", "fast", "cheap"},
			wantIDs: []string{"ollama-qwen", "gpt-4o-mini", "claude-4-sonnet", "gpt-4o"},
		},
		{
			name:    "no matching tags - empty result",
			tags:    []string{"nonexistent"},
			wantIDs: nil,
		},
		{
			name:    "empty tags - nil result",
			tags:    []string{},
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := reg.findModelsByBestMatch(tt.tags)

			if len(models) != len(tt.wantIDs) {
				gotIDs := make([]string, len(models))
				for i, m := range models {
					gotIDs[i] = m.ID
				}
				t.Errorf("findModelsByBestMatch(%v) returned %d models %v, want %d %v",
					tt.tags, len(models), gotIDs, len(tt.wantIDs), tt.wantIDs)
				return
			}

			for i, model := range models {
				if model.ID != tt.wantIDs[i] {
					t.Errorf("findModelsByBestMatch(%v)[%d].ID = %q, want %q", tt.tags, i, model.ID, tt.wantIDs[i])
				}
			}
		})
	}
}

// =============================================================================
// Resolve Tests
// =============================================================================

func TestResolve_ByExactID(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name             string
		selector         ModelSelector
		available        []string
		wantID           string
		wantProvider     string
		wantAPIModel     string
		wantErr          bool
		wantErrSubstring string
	}{
		{
			name:         "exact ID with native provider available",
			selector:     ModelSelector{ID: "claude-4-opus"},
			available:    []string{"anthropic"},
			wantID:       "claude-4-opus",
			wantProvider: "anthropic",
			wantAPIModel: "claude-opus-4-20250514",
		},
		{
			name:         "exact ID with only openrouter available",
			selector:     ModelSelector{ID: "claude-4-opus"},
			available:    []string{"openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "openrouter",
			wantAPIModel: "anthropic/claude-opus-4",
		},
		{
			name:         "exact ID with multiple providers - native wins",
			selector:     ModelSelector{ID: "claude-4-opus"},
			available:    []string{"openrouter", "anthropic"},
			wantID:       "claude-4-opus",
			wantProvider: "anthropic", // native wins over openrouter
			wantAPIModel: "claude-opus-4-20250514",
		},
		{
			name:             "exact ID with no available provider",
			selector:         ModelSelector{ID: "claude-4-opus"},
			available:        []string{"gemini"},
			wantErr:          true,
			wantErrSubstring: "no available provider",
		},
		{
			name:             "non-existent model ID",
			selector:         ModelSelector{ID: "nonexistent"},
			available:        []string{"anthropic"},
			wantErr:          true,
			wantErrSubstring: "model not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.Resolve(tt.selector, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.wantErrSubstring != "" && !containsSubstring(err.Error(), tt.wantErrSubstring) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstring)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Definition.ID != tt.wantID {
				t.Errorf("resolved model ID = %q, want %q", result.Definition.ID, tt.wantID)
			}
			if result.Provider.Driver != tt.wantProvider {
				t.Errorf("resolved provider = %q, want %q", result.Provider.Driver, tt.wantProvider)
			}
			if result.Provider.APIModel != tt.wantAPIModel {
				t.Errorf("resolved API model = %q, want %q", result.Provider.APIModel, tt.wantAPIModel)
			}
		})
	}
}

func TestResolve_BySingleTag(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name         string
		selector     ModelSelector
		available    []string
		wantID       string
		wantProvider string
		wantErr      bool
	}{
		{
			name:         "single tag - gets first model in order",
			selector:     ModelSelector{Tags: []string{"flagship"}},
			available:    []string{"anthropic", "openai"},
			wantID:       "claude-4-opus", // First flagship in YAML order
			wantProvider: "anthropic",
		},
		{
			name:         "single tag - fallback when first unavailable",
			selector:     ModelSelector{Tags: []string{"flagship"}},
			available:    []string{"openai"}, // Anthropic not available
			wantID:       "gpt-4o",           // First flagship with OpenAI provider
			wantProvider: "openai",
		},
		{
			name:         "reasoning tag",
			selector:     ModelSelector{Tags: []string{"reasoning"}},
			available:    []string{"anthropic"},
			wantID:       "claude-4-opus",
			wantProvider: "anthropic",
		},
		{
			name:      "nonexistent tag",
			selector:  ModelSelector{Tags: []string{"nonexistent"}},
			available: []string{"anthropic"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.Resolve(tt.selector, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Definition.ID != tt.wantID {
				t.Errorf("resolved model ID = %q, want %q", result.Definition.ID, tt.wantID)
			}
			if result.Provider.Driver != tt.wantProvider {
				t.Errorf("resolved provider = %q, want %q", result.Provider.Driver, tt.wantProvider)
			}
		})
	}
}

func TestResolve_CodexModerateUsesGPT55(t *testing.T) {
	reg := MustGetRegistry()

	definition, ok := reg.GetDefinition("gpt-5.5")
	require.True(t, ok, "expected gpt-5.5 to exist in registry")
	assert.Contains(t, definition.Tags, TagFlagship)
	assert.Contains(t, definition.Tags, TagModerate)

	moderate, err := reg.Resolve(ModelSelector{Tags: []string{TagModerate}}, []string{"codex"})
	require.NoError(t, err)

	flagship, err := reg.Resolve(ModelSelector{Tags: []string{TagFlagship}}, []string{"codex"})
	require.NoError(t, err)

	assert.Equal(t, "gpt-5.5", moderate.Definition.ID)
	assert.Equal(t, flagship.Definition.ID, moderate.Definition.ID)
	assert.Equal(t, flagship.Provider.Driver, moderate.Provider.Driver)
}

// Tag resolution breaks ties by YAML definition order, so adding models can
// silently repoint a tag that nothing names explicitly. This pins the codex
// tag targets so a future insertion has to change them on purpose.
func TestResolve_CodexTagTargetsArePinned(t *testing.T) {
	reg := MustGetRegistry()

	for tag, want := range map[string]string{
		TagFlagship:  "gpt-5.5",
		TagModerate:  "gpt-5.5",
		TagFast:      "gpt-5.3-codex-spark",
		TagReasoning: "gpt-5.5",
	} {
		resolved, err := reg.Resolve(ModelSelector{Tags: []string{tag}}, []string{"codex"})
		require.NoError(t, err, "resolving tag %q", tag)
		assert.Equal(t, want, resolved.Definition.ID, "codex tag %q resolved to an unexpected model", tag)
	}
}

func TestResolve_ReliantThinkingPolicyRegressionModels(t *testing.T) {
	reg := MustGetRegistry()

	tests := []struct {
		name                string
		modelID             string
		thinkingLevel       string
		wantReasoningEffort string
		wantCanReason       bool
	}{
		{
			name:          "claude haiku reliant disables explicit thinking",
			modelID:       "claude-4.5-haiku@reliant",
			thinkingLevel: "low",
		},
		{
			name:                "claude sonnet reliant keeps supported thinking",
			modelID:             "claude-4.5-sonnet@reliant",
			thinkingLevel:       "low",
			wantReasoningEffort: "low",
			wantCanReason:       true,
		},
		{
			name:                "gemini pro unsupported level falls back to supported default",
			modelID:             "gemini-3.1-pro-preview@reliant",
			thinkingLevel:       "medium",
			wantReasoningEffort: "high",
			wantCanReason:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved, err := reg.Resolve(ModelSelector{ID: tt.modelID}, []string{"reliant"})
			require.NoError(t, err)
			assert.Equal(t, "reliant", resolved.Provider.Driver)
			assert.Equal(t, tt.wantCanReason, resolved.Definition.Capabilities.CanReason)

			capability := ResolveThinkingCapability(resolved.Definition.Capabilities)
			assert.Equal(t, tt.wantReasoningEffort, ReconcileThinkingLevel(capability, tt.thinkingLevel))
		})
	}
}

func TestResolve_WithDriverSuffix(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name         string
		selector     ModelSelector
		available    []string
		wantID       string
		wantProvider string
		wantErr      bool
	}{
		{
			name:         "model ID with explicit driver suffix",
			selector:     ModelSelector{ID: "claude-4-opus@anthropic"},
			available:    []string{"anthropic", "openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "anthropic",
			wantErr:      false,
		},
		{
			name:         "model ID with suffix overrides provider selection",
			selector:     ModelSelector{ID: "claude-4-opus@openrouter"},
			available:    []string{"anthropic", "openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "openrouter", // Should use openrouter because of suffix, even though anthropic is higher priority
			wantErr:      false,
		},
		{
			name:      "model ID with suffix failing availability",
			selector:  ModelSelector{ID: "claude-4-opus@gemini"},
			available: []string{"anthropic", "openrouter"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.Resolve(tt.selector, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Definition.ID != tt.wantID {
				t.Errorf("resolved model ID = %q, want %q", result.Definition.ID, tt.wantID)
			}
			if result.Provider.Driver != tt.wantProvider {
				t.Errorf("resolved provider = %q, want %q", result.Provider.Driver, tt.wantProvider)
			}
		})
	}
}

func TestResolve_ByMultipleTags(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name         string
		selector     ModelSelector
		available    []string
		wantID       string
		wantProvider string
		wantErr      bool
	}{
		{
			// Perfect match: model has all tags
			name:         "perfect match - flagship AND fast",
			selector:     ModelSelector{Tags: []string{"flagship", "fast"}},
			available:    []string{"anthropic", "openai"},
			wantID:       "claude-4-sonnet", // Has both flagship AND fast
			wantProvider: "anthropic",
		},
		{
			name:         "perfect match - flagship AND reasoning",
			selector:     ModelSelector{Tags: []string{"flagship", "reasoning"}},
			available:    []string{"anthropic"},
			wantID:       "claude-4-opus", // Only one has both tags
			wantProvider: "anthropic",
		},
		{
			name:         "perfect match - fast AND cheap",
			selector:     ModelSelector{Tags: []string{"fast", "cheap"}},
			available:    []string{"openai", "local"},
			wantID:       "gpt-4o-mini", // First with both tags and available provider
			wantProvider: "openai",
		},
		{
			// Best-match fallback: no model has both, but flagship (weight 2) > cheap (weight 1)
			// so we get the first flagship model with an available provider
			name:         "best-match fallback - flagship preferred over cheap",
			selector:     ModelSelector{Tags: []string{"flagship", "cheap"}},
			available:    []string{"anthropic", "openai"},
			wantID:       "claude-4-opus", // First flagship model (score 2)
			wantProvider: "anthropic",
		},
		{
			// Best-match with reversed order: cheap (weight 2) > flagship (weight 1)
			name:         "best-match fallback - cheap preferred when first",
			selector:     ModelSelector{Tags: []string{"cheap", "flagship"}},
			available:    []string{"anthropic", "openai"},
			wantID:       "gpt-4o-mini", // Only cheap model (score 2)
			wantProvider: "openai",
		},
		{
			name:         "three tags - perfect match local AND fast AND cheap",
			selector:     ModelSelector{Tags: []string{"local", "fast", "cheap"}},
			available:    []string{"local"},
			wantID:       "ollama-qwen",
			wantProvider: "local",
		},
		{
			// Tag order matters: [local, fast] prefers local models
			name:         "tag order - local first prefers local model",
			selector:     ModelSelector{Tags: []string{"local", "fast"}},
			available:    []string{"local", "anthropic"},
			wantID:       "ollama-qwen", // local+fast (score 3) beats fast-only (score 1)
			wantProvider: "local",
		},
		{
			// No matching tags at all
			name:      "no matching tags",
			selector:  ModelSelector{Tags: []string{"nonexistent"}},
			available: []string{"anthropic"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.Resolve(tt.selector, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Definition.ID != tt.wantID {
				t.Errorf("resolved model ID = %q, want %q", result.Definition.ID, tt.wantID)
			}
			if result.Provider.Driver != tt.wantProvider {
				t.Errorf("resolved provider = %q, want %q", result.Provider.Driver, tt.wantProvider)
			}
		})
	}
}

func TestResolve_WithForcedProvider(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name         string
		selector     ModelSelector
		available    []string
		wantID       string
		wantProvider string
		wantErr      bool
	}{
		{
			name:         "force openrouter when native available",
			selector:     ModelSelector{ID: "claude-4-opus", Providers: []string{"openrouter"}},
			available:    []string{"anthropic", "openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "openrouter", // Forced, even though anthropic has higher priority
		},
		{
			name:         "force anthropic explicitly",
			selector:     ModelSelector{ID: "claude-4-opus", Providers: []string{"anthropic"}},
			available:    []string{"anthropic", "openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "anthropic",
		},
		{
			name:         "preferred provider not in model - falls back to available",
			selector:     ModelSelector{ID: "claude-4-opus", Providers: []string{"openai"}},
			available:    []string{"anthropic", "openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "anthropic", // OpenAI doesn't provide Claude, fallback to best available
		},
		{
			name:         "preferred provider not in available list - falls back",
			selector:     ModelSelector{ID: "claude-4-opus", Providers: []string{"anthropic"}},
			available:    []string{"openrouter"},
			wantID:       "claude-4-opus",
			wantProvider: "openrouter", // Anthropic not available, fallback to openrouter
		},
		{
			name:      "no providers available at all",
			selector:  ModelSelector{ID: "claude-4-opus", Providers: []string{"local"}},
			available: []string{"gemini"}, // Claude doesn't have gemini provider
			wantErr:   true,
		},
		{
			name:         "force provider with tag resolution",
			selector:     ModelSelector{Tags: []string{"flagship"}, Providers: []string{"openrouter"}},
			available:    []string{"anthropic", "openrouter"},
			wantID:       "claude-4-opus", // First flagship with openrouter
			wantProvider: "openrouter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.Resolve(tt.selector, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Definition.ID != tt.wantID {
				t.Errorf("resolved model ID = %q, want %q", result.Definition.ID, tt.wantID)
			}
			if result.Provider.Driver != tt.wantProvider {
				t.Errorf("resolved provider = %q, want %q", result.Provider.Driver, tt.wantProvider)
			}
		})
	}
}

func TestResolve_InvalidSelector(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name     string
		selector ModelSelector
	}{
		{
			name:     "empty selector",
			selector: ModelSelector{},
		},
		{
			name:     "only provider set",
			selector: ModelSelector{Providers: []string{"anthropic"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := reg.Resolve(tt.selector, []string{"anthropic"})
			if err == nil {
				t.Error("expected error for invalid selector, got nil")
			}
		})
	}
}

// =============================================================================
// Provider Priority Tests
// =============================================================================

func TestProviderPriority(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name         string
		modelID      string
		available    []string
		wantProvider string
	}{
		{
			name:         "native anthropic beats openrouter",
			modelID:      "claude-4-opus",
			available:    []string{"openrouter", "anthropic"},
			wantProvider: "anthropic",
		},
		{
			name:         "native openai beats openrouter",
			modelID:      "gpt-4o",
			available:    []string{"openrouter", "openai"},
			wantProvider: "openai",
		},
		{
			name:         "openrouter when native not available",
			modelID:      "claude-4-opus",
			available:    []string{"openrouter"},
			wantProvider: "openrouter",
		},
		{
			name:         "local provider used when specified",
			modelID:      "ollama-qwen",
			available:    []string{"local", "openrouter"},
			wantProvider: "local",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.Resolve(ModelSelector{ID: tt.modelID}, tt.available)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Provider.Driver != tt.wantProvider {
				t.Errorf("got provider %q, want %q", result.Provider.Driver, tt.wantProvider)
			}
		})
	}
}

// =============================================================================
// Clone Tests
// =============================================================================

func TestClone(t *testing.T) {
	original := createTestRegistry(t)
	cloned := original.Clone()

	// Verify clone has same data
	if len(cloned.models) != len(original.models) {
		t.Errorf("cloned has %d models, original has %d", len(cloned.models), len(original.models))
	}

	// Verify clone is independent - modify original shouldn't affect clone
	originalModel, _ := original.GetDefinition("claude-4-opus")
	originalModel.Name = "Modified Name"

	clonedModel, _ := cloned.GetDefinition("claude-4-opus")
	if clonedModel.Name == "Modified Name" {
		t.Error("modifying original affected clone - not a deep copy")
	}

	// Verify tag indices work correctly
	originalTags := original.GetModelsByTag("flagship")
	clonedTags := cloned.GetModelsByTag("flagship")
	if len(originalTags) != len(clonedTags) {
		t.Errorf("tag indices differ: original has %d, cloned has %d", len(originalTags), len(clonedTags))
	}
}

// =============================================================================
// ResolveWithFallback Tests
// =============================================================================

func TestResolveWithFallback(t *testing.T) {
	reg := createTestRegistry(t)

	tests := []struct {
		name      string
		primary   ModelSelector
		fallback  ModelSelector
		available []string
		wantID    string
		wantErr   bool
	}{
		{
			name:      "primary succeeds",
			primary:   ModelSelector{ID: "claude-4-opus"},
			fallback:  ModelSelector{Tags: []string{"fast"}},
			available: []string{"anthropic"},
			wantID:    "claude-4-opus",
		},
		{
			name:      "primary fails, fallback succeeds",
			primary:   ModelSelector{ID: "nonexistent"},
			fallback:  ModelSelector{Tags: []string{"fast"}},
			available: []string{"anthropic"},
			wantID:    "claude-4-sonnet", // First fast model with anthropic
		},
		{
			name:      "primary fails due to provider, fallback succeeds",
			primary:   ModelSelector{ID: "ollama-qwen"}, // Only has local provider
			fallback:  ModelSelector{Tags: []string{"fast"}},
			available: []string{"anthropic"},
			wantID:    "claude-4-sonnet",
		},
		{
			name:      "both fail",
			primary:   ModelSelector{ID: "nonexistent"},
			fallback:  ModelSelector{Tags: []string{"nonexistent-tag"}},
			available: []string{"anthropic"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := reg.ResolveWithFallback(tt.primary, tt.fallback, tt.available)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Definition.ID != tt.wantID {
				t.Errorf("resolved model ID = %q, want %q", result.Definition.ID, tt.wantID)
			}
		})
	}
}

// =============================================================================
// ListAllTags Tests
// =============================================================================

func TestListAllTags(t *testing.T) {
	reg := createTestRegistry(t)

	tags := reg.ListAllTags()

	expectedTags := []string{"cheap", "fast", "flagship", "local", "meta", "moderate", "reasoning"}

	if len(tags) != len(expectedTags) {
		t.Errorf("got %d tags, want %d", len(tags), len(expectedTags))
	}

	// Tags should be sorted
	for i, tag := range tags {
		if tag != expectedTags[i] {
			t.Errorf("tag[%d] = %q, want %q", i, tag, expectedTags[i])
		}
	}
}

// =============================================================================
// User Config Tests
// =============================================================================

func TestLoadUserModelsConfig_MissingFile(t *testing.T) {
	cfg, err := LoadUserModelsConfig("/nonexistent/path/to/config.yaml")
	if err != nil {
		t.Errorf("expected nil error for missing file, got: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config for missing file, got: %+v", cfg)
	}
}

func TestLoadUserModelsConfigFromBytes(t *testing.T) {
	yaml := `
tag_preferences:
  flagship:
    - gpt-4o
    - claude-4-opus
custom:
  - id: my-custom-model
    name: My Custom Model
    tags: [custom]
    visibility: user
    providers:
      - driver: local
        api_model: my-model:latest
`

	cfg, err := LoadUserModelsConfigFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.TagPreferences) != 1 {
		t.Errorf("expected 1 tag preference, got %d", len(cfg.TagPreferences))
	}

	if len(cfg.TagPreferences["flagship"]) != 2 {
		t.Errorf("expected 2 flagship preferences, got %d", len(cfg.TagPreferences["flagship"]))
	}

	if len(cfg.Custom) != 1 {
		t.Errorf("expected 1 custom model, got %d", len(cfg.Custom))
	}

	if cfg.Custom[0].ID != "my-custom-model" {
		t.Errorf("expected custom model ID 'my-custom-model', got %q", cfg.Custom[0].ID)
	}
}

func TestMergeUserConfig_AddCustomModels(t *testing.T) {
	reg := createTestRegistry(t)

	cfg := &UserModelsConfig{
		Custom: []ModelDefinition{
			{
				ID:         "my-custom-model",
				Name:       "My Custom Model",
				Tags:       []string{"custom", "fast"},
				Visibility: VisibilityUser,
				Providers: []ProviderMapping{
					{Driver: "local", APIModel: "my-model:latest"},
				},
			},
		},
	}

	err := reg.MergeUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify custom model was added
	model, ok := reg.GetDefinition("my-custom-model")
	if !ok {
		t.Fatal("custom model not found after merge")
	}
	if model.Name != "My Custom Model" {
		t.Errorf("custom model name = %q, want 'My Custom Model'", model.Name)
	}

	// Verify it's indexed by tags
	fastModels := reg.GetModelsByTag("fast")
	found := false
	for _, m := range fastModels {
		if m.ID == "my-custom-model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom model not found in 'fast' tag index")
	}

	// Verify custom tag was created
	customModels := reg.GetModelsByTag("custom")
	if len(customModels) != 1 || customModels[0].ID != "my-custom-model" {
		t.Errorf("custom tag not indexed correctly, got %d models", len(customModels))
	}
}

func TestMergeUserConfig_ApplyTagPreferences(t *testing.T) {
	reg := createTestRegistry(t)

	// Original order: claude-4-opus, claude-4-sonnet, gpt-4o
	originalOrder := []string{"claude-4-opus", "claude-4-sonnet", "gpt-4o"}
	flagshipModels := reg.GetModelsByTag("flagship")
	for i, m := range flagshipModels {
		if m.ID != originalOrder[i] {
			t.Fatalf("unexpected original order: got %v", getModelIDs(flagshipModels))
		}
	}

	cfg := &UserModelsConfig{
		TagPreferences: map[string][]string{
			"flagship": {"gpt-4o", "claude-4-sonnet"}, // Reorder: gpt-4o first, then claude-4-sonnet
		},
	}

	err := reg.MergeUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// New order should be: gpt-4o, claude-4-sonnet, claude-4-opus
	flagshipModels = reg.GetModelsByTag("flagship")
	expectedOrder := []string{"gpt-4o", "claude-4-sonnet", "claude-4-opus"}
	if len(flagshipModels) != len(expectedOrder) {
		t.Fatalf("expected %d flagship models, got %d", len(expectedOrder), len(flagshipModels))
	}
	for i, m := range flagshipModels {
		if m.ID != expectedOrder[i] {
			t.Errorf("flagship[%d] = %q, want %q", i, m.ID, expectedOrder[i])
		}
	}
}

func TestMergeUserConfig_DuplicateIDError(t *testing.T) {
	reg := createTestRegistry(t)

	cfg := &UserModelsConfig{
		Custom: []ModelDefinition{
			{
				ID:   "claude-4-opus", // Conflicts with existing
				Name: "Duplicate",
				Providers: []ProviderMapping{
					{Driver: "local", APIModel: "test"},
				},
			},
		},
	}

	err := reg.MergeUserConfig(cfg)
	if err == nil {
		t.Error("expected error for duplicate ID, got nil")
	}
}

func TestMergeUserConfig_MissingIDError(t *testing.T) {
	reg := createTestRegistry(t)

	cfg := &UserModelsConfig{
		Custom: []ModelDefinition{
			{
				Name: "No ID",
				Providers: []ProviderMapping{
					{Driver: "local", APIModel: "test"},
				},
			},
		},
	}

	err := reg.MergeUserConfig(cfg)
	if err == nil {
		t.Error("expected error for missing ID, got nil")
	}
}

func TestMergeUserConfig_MissingProvidersError(t *testing.T) {
	reg := createTestRegistry(t)

	cfg := &UserModelsConfig{
		Custom: []ModelDefinition{
			{
				ID:   "no-providers",
				Name: "No Providers",
			},
		},
	}

	err := reg.MergeUserConfig(cfg)
	if err == nil {
		t.Error("expected error for missing providers, got nil")
	}
}

func TestMergeUserConfig_NilConfig(t *testing.T) {
	reg := createTestRegistry(t)
	originalCount := len(reg.models)

	err := reg.MergeUserConfig(nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(reg.models) != originalCount {
		t.Errorf("model count changed with nil config: %d -> %d", originalCount, len(reg.models))
	}
}

func TestMergeUserConfig_DefaultsApplied(t *testing.T) {
	reg := createTestRegistry(t)

	cfg := &UserModelsConfig{
		Custom: []ModelDefinition{
			{
				ID: "minimal-model",
				// Name and Visibility not set - should be defaulted
				Providers: []ProviderMapping{
					{Driver: "local", APIModel: "test"},
				},
			},
		},
	}

	err := reg.MergeUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	model, _ := reg.GetDefinition("minimal-model")
	if model.Name != "minimal-model" {
		t.Errorf("name not defaulted to ID, got %q", model.Name)
	}
	if model.Visibility != VisibilityUser {
		t.Errorf("visibility not defaulted to user, got %q", model.Visibility)
	}
}

// =============================================================================
// ValidateUserConfig Tests
// =============================================================================

func TestValidateUserConfig(t *testing.T) {
	tests := []struct {
		name         string
		configYAML   string
		wantErr      bool
		wantWarnings int
	}{
		{
			name:       "nil config",
			configYAML: "",
			wantErr:    false,
		},
		{
			name: "valid config",
			configYAML: `
custom:
  - id: custom-model
    name: Custom
    providers:
      - driver: local
        api_model: test
`,
			wantErr: false,
		},
		{
			name: "duplicate custom IDs",
			configYAML: `
custom:
  - id: custom-model
    providers:
      - driver: local
        api_model: test1
  - id: custom-model
    providers:
      - driver: local
        api_model: test2
`,
			wantErr: true,
		},
		{
			name: "custom ID conflicts with built-in",
			configYAML: `
custom:
  - id: claude-4.6-opus
    providers:
      - driver: local
        api_model: test
`,
			wantErr: true,
		},
		{
			name: "unknown model in tag preference - warning only",
			configYAML: `
tag_preferences:
  flagship:
    - unknown-model
`,
			wantErr:      false,
			wantWarnings: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *UserModelsConfig
			if tt.configYAML != "" {
				var err error
				cfg, err = LoadUserModelsConfigFromBytes([]byte(tt.configYAML))
				if err != nil {
					t.Fatalf("failed to parse test config: %v", err)
				}
			}

			warnings, err := ValidateUserConfig(cfg)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(warnings) != tt.wantWarnings {
				t.Errorf("got %d warnings, want %d: %v", len(warnings), tt.wantWarnings, warnings)
			}
		})
	}
}

// =============================================================================
// Legacy Registry Integration Tests
// =============================================================================

func TestDefinitionToModel(t *testing.T) {
	def := ModelDefinition{
		ID:   "test-model",
		Name: "Test Model",
		Tags: []string{"flagship"},
		Capabilities: ModelCapabilities{
			CanReason:           true,
			SupportsTools:       true,
			SupportsAttachments: true,
			SupportsStreaming:   true,
			SupportsCaching:     true,
			MaxContextWindow:    200000,
			MaxOutputTokens:     32000,
		},
		Cost: ModelCost{
			InputPer1M:       15.0,
			OutputPer1M:      75.0,
			CachedInputPer1M: 1.5,
		},
		Providers: []ProviderMapping{
			{Driver: "anthropic", APIModel: "claude-test"},
		},
		Visibility: VisibilityUser,
	}

	model := def.ToModel()

	if model.ID != ModelID("test-model") {
		t.Errorf("ID = %q, want 'test-model'", model.ID)
	}
	if model.Name != "Test Model" {
		t.Errorf("Name = %q, want 'Test Model'", model.Name)
	}
	if model.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", model.ContextWindow)
	}
	if model.DefaultMaxTokens != 32000 {
		t.Errorf("DefaultMaxTokens = %d, want 32000", model.DefaultMaxTokens)
	}
	if model.CostPer1MIn != 15.0 {
		t.Errorf("CostPer1MIn = %f, want 15.0", model.CostPer1MIn)
	}
	if model.CostPer1MOut != 75.0 {
		t.Errorf("CostPer1MOut = %f, want 75.0", model.CostPer1MOut)
	}
	if model.CostPer1MInCached != 1.5 {
		t.Errorf("CostPer1MInCached = %f, want 1.5", model.CostPer1MInCached)
	}
	if !model.CanReason {
		t.Error("CanReason = false, want true")
	}
	if !model.SupportsAttachments {
		t.Error("SupportsAttachments = false, want true")
	}
	if model.APIModel != "claude-test" {
		t.Errorf("APIModel = %q, want 'claude-test'", model.APIModel)
	}
	if model.DevOnly {
		t.Error("DevOnly = true, want false for user visibility")
	}
}

func TestDefinitionToModel_DevVisibility(t *testing.T) {
	def := ModelDefinition{
		ID:         "dev-model",
		Name:       "Dev Model",
		Visibility: VisibilityDev,
		Providers: []ProviderMapping{
			{Driver: "local", APIModel: "test"},
		},
	}

	model := def.ToModel()

	if !model.DevOnly {
		t.Error("DevOnly = false, want true for dev visibility")
	}
}

func TestDefinitionToModel_DriverSettings(t *testing.T) {
	def := ModelDefinition{
		ID:   "openai-model",
		Name: "OpenAI Model",
		Providers: []ProviderMapping{
			{Driver: "openai", APIModel: "o1-preview"},
		},
		DriverSettings: &DriverSettings{
			PreferredEndpoint:      "responses",
			TemperatureMode:        "omit",
			UseMaxCompletionTokens: true,
			ReasoningSummaryMode:   "auto",
		},
	}

	model := def.ToModel()

	if model.PreferredEndpoint != "responses" {
		t.Errorf("PreferredEndpoint = %q, want 'responses'", model.PreferredEndpoint)
	}
	if model.TemperatureMode != TemperatureModeOmit {
		t.Errorf("TemperatureMode = %v, want TemperatureModeOmit", model.TemperatureMode)
	}
	if !model.UseMaxCompletionTokens {
		t.Error("UseMaxCompletionTokens = false, want true")
	}
	if model.ReasoningSummaryMode != ReasoningSummaryMode("auto") {
		t.Errorf("ReasoningSummaryMode = %q, want 'auto'", model.ReasoningSummaryMode)
	}
}

// =============================================================================
// LoadUserModelsConfig with real file
// =============================================================================

func TestLoadUserModelsConfig_RealFile(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "models.yaml")

	configYAML := `
tag_preferences:
  flagship:
    - gpt-4o
custom:
  - id: test-local
    name: Test Local Model
    tags: [local]
    visibility: user
    providers:
      - driver: local
        api_model: test:latest
`

	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadUserModelsConfig(configPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	if len(cfg.TagPreferences["flagship"]) != 1 {
		t.Errorf("expected 1 flagship preference, got %d", len(cfg.TagPreferences["flagship"]))
	}
	if len(cfg.Custom) != 1 {
		t.Errorf("expected 1 custom model, got %d", len(cfg.Custom))
	}
}

// =============================================================================
// CreateRegistryWithUserConfig Tests
// =============================================================================

func TestCreateRegistryWithUserConfig_NilConfig(t *testing.T) {
	reg, err := CreateRegistryWithUserConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}

	// Should return the base registry unmodified
	baseReg, _ := GetRegistry()
	if len(reg.models) != len(baseReg.models) {
		t.Errorf("expected same number of models as base registry")
	}
}

func TestCreateRegistryWithUserConfig_WithCustomModels(t *testing.T) {
	cfg := &UserModelsConfig{
		Custom: []ModelDefinition{
			{
				ID:         "user-custom-model",
				Name:       "User Custom Model",
				Tags:       []string{"custom"},
				Visibility: VisibilityUser,
				Providers: []ProviderMapping{
					{Driver: "local", APIModel: "custom:latest"},
				},
			},
		},
	}

	reg, err := CreateRegistryWithUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Custom model should be in the new registry
	model, ok := reg.GetDefinition("user-custom-model")
	if !ok {
		t.Fatal("custom model not found in registry")
	}
	if model.Name != "User Custom Model" {
		t.Errorf("custom model name = %q, want 'User Custom Model'", model.Name)
	}

	// Base registry should NOT have the custom model
	baseReg, _ := GetRegistry()
	if _, ok := baseReg.GetDefinition("user-custom-model"); ok {
		t.Error("custom model should not be in base registry")
	}
}

func TestCreateRegistryWithUserConfig_WithTagPreferences(t *testing.T) {
	// Get baseline order first
	baseReg, _ := GetRegistry()
	baseFlagship := baseReg.GetModelsByTag("flagship")
	if len(baseFlagship) < 2 {
		t.Skip("need at least 2 flagship models for this test")
	}

	// Reorder so second model comes first
	cfg := &UserModelsConfig{
		TagPreferences: map[string][]string{
			"flagship": {baseFlagship[1].ID, baseFlagship[0].ID},
		},
	}

	reg, err := CreateRegistryWithUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check reordering worked
	newFlagship := reg.GetModelsByTag("flagship")
	if newFlagship[0].ID != baseFlagship[1].ID {
		t.Errorf("first flagship = %q, want %q", newFlagship[0].ID, baseFlagship[1].ID)
	}
	if newFlagship[1].ID != baseFlagship[0].ID {
		t.Errorf("second flagship = %q, want %q", newFlagship[1].ID, baseFlagship[0].ID)
	}

	// Base registry should be unchanged
	baseAfter := baseReg.GetModelsByTag("flagship")
	if baseAfter[0].ID != baseFlagship[0].ID {
		t.Error("base registry was modified")
	}
}

// =============================================================================
// ParseRegistry Tests (embedded YAML)
// =============================================================================

func TestParseRegistry_EmbeddedYAML(t *testing.T) {
	// This tests that the actual embedded models.yaml parses correctly
	reg, err := ParseRegistry()
	if err != nil {
		t.Fatalf("failed to parse embedded YAML: %v", err)
	}

	// Should have at least some models
	if len(reg.models) == 0 {
		t.Error("expected at least one model in embedded YAML")
	}

	// Should have some common tags
	flagshipModels := reg.GetModelsByTag("flagship")
	if len(flagshipModels) == 0 {
		t.Error("expected at least one flagship model")
	}

	// All models should have required fields
	for _, model := range reg.models {
		if model.ID == "" {
			t.Error("found model with empty ID")
		}
		if model.Name == "" {
			t.Errorf("model %s has empty name", model.ID)
		}
		if len(model.Providers) == 0 {
			t.Errorf("model %s has no providers", model.ID)
		}
		if model.Visibility == "" {
			t.Errorf("model %s has empty visibility", model.ID)
		}
		if !model.Capabilities.CanReason && model.DefaultThinkingLevel != "" {
			t.Errorf("model %s has default_thinking_level %q but does not support thinking", model.ID, model.DefaultThinkingLevel)
		}
	}
}

func TestGetRegistry_Singleton(t *testing.T) {
	// GetRegistry should return the same instance
	reg1, err1 := GetRegistry()
	reg2, err2 := GetRegistry()

	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}

	if reg1 != reg2 {
		t.Error("GetRegistry should return the same instance")
	}
}

// =============================================================================
// Edge Case Tests
// =============================================================================

func TestResolve_EmptyAvailableProviders(t *testing.T) {
	reg := createTestRegistry(t)

	_, err := reg.Resolve(ModelSelector{ID: "claude-4-opus"}, []string{})
	if err == nil {
		t.Error("expected error with empty available providers")
	}
}

func TestResolve_UnknownProviderPriority(t *testing.T) {
	// Test with a provider not in ProviderPriority map
	yaml := `
models:
  - id: test-model
    name: Test Model
    tags: [test]
    visibility: user
    providers:
      - driver: unknown_provider
        api_model: test
`
	reg, err := ParseRegistryFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	result, err := reg.Resolve(ModelSelector{ID: "test-model"}, []string{"unknown_provider"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Provider.Driver != "unknown_provider" {
		t.Errorf("expected unknown_provider, got %s", result.Provider.Driver)
	}
}

func TestMergeUserConfig_TagPreferenceUnknownModel(t *testing.T) {
	reg := createTestRegistry(t)

	cfg := &UserModelsConfig{
		TagPreferences: map[string][]string{
			"flagship": {"nonexistent-model", "claude-4-opus"},
		},
	}

	// Should error because nonexistent-model doesn't exist
	err := reg.MergeUserConfig(cfg)
	if err == nil {
		t.Error("expected error for unknown model in tag preference")
	}
}

func TestMergeUserConfig_TagPreferenceModelWithoutTag(t *testing.T) {
	reg := createTestRegistry(t)

	// ollama-qwen exists but doesn't have 'flagship' tag
	cfg := &UserModelsConfig{
		TagPreferences: map[string][]string{
			"flagship": {"ollama-qwen", "claude-4-opus"},
		},
	}

	// Should succeed - model without tag is silently skipped
	err := reg.MergeUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// claude-4-opus should still be first since ollama-qwen was skipped
	flagship := reg.GetModelsByTag("flagship")
	if flagship[0].ID != "claude-4-opus" {
		t.Errorf("first flagship = %q, want claude-4-opus", flagship[0].ID)
	}
}

func TestMergeUserConfig_EmptyTagPreference(t *testing.T) {
	reg := createTestRegistry(t)

	// Empty preferences for a tag - should be no-op
	cfg := &UserModelsConfig{
		TagPreferences: map[string][]string{
			"flagship": {},
		},
	}

	originalOrder := getModelIDs(reg.GetModelsByTag("flagship"))

	err := reg.MergeUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	newOrder := getModelIDs(reg.GetModelsByTag("flagship"))
	for i := range originalOrder {
		if originalOrder[i] != newOrder[i] {
			t.Errorf("order changed unexpectedly at index %d", i)
		}
	}
}

func TestMergeUserConfig_NonexistentTag(t *testing.T) {
	reg := createTestRegistry(t)

	// Preference for a tag that doesn't exist yet
	cfg := &UserModelsConfig{
		TagPreferences: map[string][]string{
			"nonexistent-tag": {"claude-4-opus"},
		},
	}

	// Should succeed silently - tag doesn't exist so nothing to reorder
	err := reg.MergeUserConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// =============================================================================
// Helper functions
// =============================================================================

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (s[:len(substr)] == substr || containsSubstring(s[1:], substr)))
}

func getModelIDs(models []*ModelDefinition) []string {
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

// =============================================================================
// DriverSettings Parsing Tests
// =============================================================================

func TestParseDriverSettings(t *testing.T) {
	tests := []struct {
		name                       string
		yaml                       string
		wantPreferredEndpoint      string
		wantTemperatureMode        string
		wantUseMaxCompletionTokens bool
		wantReasoningSummaryMode   string
		wantDriverSettingsNil      bool
	}{
		{
			name: "model without driver_settings",
			yaml: `
models:
  - id: basic-model
    name: Basic Model
    tags: [test]
    visibility: user
    providers:
      - driver: openai
        api_model: gpt-basic
`,
			wantDriverSettingsNil: true,
		},
		{
			name: "model with preferred_endpoint only",
			yaml: `
models:
  - id: openai-o1
    name: OpenAI o1
    tags: [reasoning]
    visibility: user
    providers:
      - driver: openai
        api_model: o1-preview
    driver_settings:
      preferred_endpoint: responses
`,
			wantPreferredEndpoint: "responses",
			wantDriverSettingsNil: false,
		},
		{
			name: "model with temperature_mode omit",
			yaml: `
models:
  - id: no-temp-model
    name: No Temperature Model
    tags: [reasoning]
    visibility: user
    providers:
      - driver: openai
        api_model: o1
    driver_settings:
      temperature_mode: omit
`,
			wantTemperatureMode:   "omit",
			wantDriverSettingsNil: false,
		},
		{
			name: "model with temperature_mode any",
			yaml: `
models:
  - id: any-temp-model
    name: Any Temperature Model
    tags: [test]
    visibility: user
    providers:
      - driver: openai
        api_model: gpt-4
    driver_settings:
      temperature_mode: any
`,
			wantTemperatureMode:   "any",
			wantDriverSettingsNil: false,
		},
		{
			name: "model with reasoning_summary_mode",
			yaml: `
models:
  - id: reasoning-model
    name: Reasoning Model
    tags: [reasoning]
    visibility: user
    providers:
      - driver: openai
        api_model: o3
    driver_settings:
      reasoning_summary_mode: auto
`,
			wantReasoningSummaryMode: "auto",
			wantDriverSettingsNil:    false,
		},
		{
			name: "model with use_max_completion_tokens",
			yaml: `
models:
  - id: completion-tokens-model
    name: Max Completion Tokens Model
    tags: [test]
    visibility: user
    providers:
      - driver: openai
        api_model: gpt-4-turbo
    driver_settings:
      use_max_completion_tokens: true
`,
			wantUseMaxCompletionTokens: true,
			wantDriverSettingsNil:      false,
		},
		{
			name: "model with all driver_settings",
			yaml: `
models:
  - id: full-settings-model
    name: Full Settings Model
    tags: [reasoning]
    visibility: user
    providers:
      - driver: openai
        api_model: o1
    driver_settings:
      preferred_endpoint: responses
      temperature_mode: omit
      use_max_completion_tokens: true
      reasoning_summary_mode: auto
`,
			wantPreferredEndpoint:      "responses",
			wantTemperatureMode:        "omit",
			wantUseMaxCompletionTokens: true,
			wantReasoningSummaryMode:   "auto",
			wantDriverSettingsNil:      false,
		},
		{
			name: "model with chat_completions endpoint",
			yaml: `
models:
  - id: chat-model
    name: Chat Model
    tags: [test]
    visibility: user
    providers:
      - driver: openai
        api_model: gpt-4
    driver_settings:
      preferred_endpoint: chat_completions
`,
			wantPreferredEndpoint: "chat_completions",
			wantDriverSettingsNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := ParseRegistryFromBytes([]byte(tt.yaml))
			if err != nil {
				t.Fatalf("failed to parse YAML: %v", err)
			}

			// Get the first (and only) model
			models := reg.ListAll()
			if len(models) != 1 {
				t.Fatalf("expected 1 model, got %d", len(models))
			}
			model := models[0]

			if tt.wantDriverSettingsNil {
				if model.DriverSettings != nil {
					t.Errorf("expected nil DriverSettings, got %+v", model.DriverSettings)
				}
				return
			}

			if model.DriverSettings == nil {
				t.Fatal("expected non-nil DriverSettings")
			}

			if model.DriverSettings.PreferredEndpoint != tt.wantPreferredEndpoint {
				t.Errorf("PreferredEndpoint = %q, want %q", model.DriverSettings.PreferredEndpoint, tt.wantPreferredEndpoint)
			}
			if model.DriverSettings.TemperatureMode != tt.wantTemperatureMode {
				t.Errorf("TemperatureMode = %q, want %q", model.DriverSettings.TemperatureMode, tt.wantTemperatureMode)
			}
			if model.DriverSettings.UseMaxCompletionTokens != tt.wantUseMaxCompletionTokens {
				t.Errorf("UseMaxCompletionTokens = %v, want %v", model.DriverSettings.UseMaxCompletionTokens, tt.wantUseMaxCompletionTokens)
			}
			if model.DriverSettings.ReasoningSummaryMode != tt.wantReasoningSummaryMode {
				t.Errorf("ReasoningSummaryMode = %q, want %q", model.DriverSettings.ReasoningSummaryMode, tt.wantReasoningSummaryMode)
			}
		})
	}
}

func TestDriverSettingsToModelConversion(t *testing.T) {
	tests := []struct {
		name                  string
		driverSettings        *DriverSettings
		wantPreferredEndpoint string
		wantTemperatureMode   TemperatureMode
		wantUseMaxCompletion  bool
		wantReasoningSummary  ReasoningSummaryMode
	}{
		{
			name:                  "nil driver settings uses zero values",
			driverSettings:        nil,
			wantPreferredEndpoint: "",
			wantTemperatureMode:   "", // zero value, not explicitly set
			wantUseMaxCompletion:  false,
			wantReasoningSummary:  "",
		},
		{
			name: "temperature_mode omit converts correctly",
			driverSettings: &DriverSettings{
				TemperatureMode: "omit",
			},
			wantTemperatureMode: TemperatureModeOmit,
		},
		{
			name: "temperature_mode any converts correctly",
			driverSettings: &DriverSettings{
				TemperatureMode: "any",
			},
			wantTemperatureMode: TemperatureModeAny,
		},
		{
			name: "empty temperature_mode defaults to any",
			driverSettings: &DriverSettings{
				TemperatureMode: "",
			},
			wantTemperatureMode: TemperatureModeAny,
		},
		{
			name: "all settings convert correctly",
			driverSettings: &DriverSettings{
				PreferredEndpoint:      "responses",
				TemperatureMode:        "omit",
				UseMaxCompletionTokens: true,
				ReasoningSummaryMode:   "auto",
			},
			wantPreferredEndpoint: "responses",
			wantTemperatureMode:   TemperatureModeOmit,
			wantUseMaxCompletion:  true,
			wantReasoningSummary:  "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := ModelDefinition{
				ID:   "test-model",
				Name: "Test Model",
				Providers: []ProviderMapping{
					{Driver: "openai", APIModel: "test"},
				},
				DriverSettings: tt.driverSettings,
			}

			model := def.ToModel()

			if model.PreferredEndpoint != tt.wantPreferredEndpoint {
				t.Errorf("PreferredEndpoint = %q, want %q", model.PreferredEndpoint, tt.wantPreferredEndpoint)
			}
			if model.TemperatureMode != tt.wantTemperatureMode {
				t.Errorf("TemperatureMode = %v, want %v", model.TemperatureMode, tt.wantTemperatureMode)
			}
			if model.UseMaxCompletionTokens != tt.wantUseMaxCompletion {
				t.Errorf("UseMaxCompletionTokens = %v, want %v", model.UseMaxCompletionTokens, tt.wantUseMaxCompletion)
			}
			if model.ReasoningSummaryMode != tt.wantReasoningSummary {
				t.Errorf("ReasoningSummaryMode = %q, want %q", model.ReasoningSummaryMode, tt.wantReasoningSummary)
			}
		})
	}
}

// =============================================================================
// Concurrent Registry Access Tests
// =============================================================================

func TestGetRegistry_Concurrent(t *testing.T) {
	// Test that multiple goroutines can call GetRegistry safely
	// Run with -race to detect race conditions
	const numGoroutines = 100

	results := make(chan *ModelRegistry, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			reg, err := GetRegistry()
			if err != nil {
				errors <- err
				return
			}
			results <- reg
		}()
	}

	// Collect all results
	var firstReg *ModelRegistry
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errors:
			t.Fatalf("GetRegistry returned error: %v", err)
		case reg := <-results:
			if firstReg == nil {
				firstReg = reg
			} else if reg != firstReg {
				t.Error("GetRegistry returned different instances across goroutines")
			}
		}
	}
}

func TestResolve_Concurrent(t *testing.T) {
	// Test that multiple goroutines can call Resolve safely
	// Run with -race to detect race conditions
	reg := createTestRegistry(t)

	const numGoroutines = 100

	selectors := []ModelSelector{
		{ID: "claude-4-opus"},
		{ID: "claude-4-sonnet"},
		{ID: "gpt-4o"},
		{Tags: []string{"flagship"}},
		{Tags: []string{"fast"}},
		{Tags: []string{"flagship", "fast"}},
	}
	available := []string{"anthropic", "openai", "openrouter"}

	errors := make(chan error, numGoroutines)
	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			selector := selectors[idx%len(selectors)]
			_, err := reg.Resolve(selector, available)
			if err != nil {
				errors <- err
				return
			}
			done <- true
		}(i)
	}

	// Collect all results
	for i := 0; i < numGoroutines; i++ {
		select {
		case err := <-errors:
			t.Fatalf("Resolve returned error: %v", err)
		case <-done:
			// success
		}
	}
}

func TestRegistryReadOperations_Concurrent(t *testing.T) {
	// Test that multiple read operations can run concurrently
	// Run with -race to detect race conditions
	reg := createTestRegistry(t)

	const numGoroutines = 50
	done := make(chan bool, numGoroutines*3) // 3 operations per goroutine

	for i := 0; i < numGoroutines; i++ {
		go func() {
			// GetDefinition
			reg.GetDefinition("claude-4-opus")
			done <- true

			// GetModelsByTag
			reg.GetModelsByTag("flagship")
			done <- true

			// ListAllTags
			reg.ListAllTags()
			done <- true
		}()
	}

	// Wait for all operations to complete
	for i := 0; i < numGoroutines*3; i++ {
		<-done
	}
}

// =============================================================================
// User Config File Loading Integration Tests
// =============================================================================

func TestLoadUserModelsConfig_IntegrationTests(t *testing.T) {
	tests := []struct {
		name             string
		configYAML       string
		wantErr          bool
		wantErrSubstring string
		validate         func(t *testing.T, cfg *UserModelsConfig)
	}{
		{
			name: "valid config with custom model",
			configYAML: `
tag_preferences:
  flagship:
    - gpt-4o
custom:
  - id: my-local-model
    name: My Local Model
    tags: [local, fast]
    visibility: user
    capabilities:
      max_context_window: 32000
      max_output_tokens: 4096
      supports_tools: true
    cost:
      input_per_1m: 0.0
      output_per_1m: 0.0
    providers:
      - driver: local
        api_model: my-model:latest
providers:
  local:
    base_url: http://localhost:11434/v1
`,
			wantErr: false,
			validate: func(t *testing.T, cfg *UserModelsConfig) {
				if len(cfg.TagPreferences["flagship"]) != 1 {
					t.Errorf("expected 1 flagship preference, got %d", len(cfg.TagPreferences["flagship"]))
				}
				if len(cfg.Custom) != 1 {
					t.Errorf("expected 1 custom model, got %d", len(cfg.Custom))
				}
				if cfg.Custom[0].ID != "my-local-model" {
					t.Errorf("custom model ID = %q, want 'my-local-model'", cfg.Custom[0].ID)
				}
				if cfg.Custom[0].Capabilities.MaxContextWindow != 32000 {
					t.Errorf("MaxContextWindow = %d, want 32000", cfg.Custom[0].Capabilities.MaxContextWindow)
				}
				if cfg.Providers.Local == nil {
					t.Error("expected local provider config")
				} else if cfg.Providers.Local.BaseURL != "http://localhost:11434/v1" {
					t.Errorf("BaseURL = %q, want 'http://localhost:11434/v1'", cfg.Providers.Local.BaseURL)
				}
			},
		},
		{
			name: "valid config with multiple tag preferences",
			configYAML: `
tag_preferences:
  flagship:
    - claude-4-opus
    - gpt-4o
  fast:
    - gpt-4o-mini
    - claude-4-sonnet
  reasoning:
    - claude-4-opus
`,
			wantErr: false,
			validate: func(t *testing.T, cfg *UserModelsConfig) {
				if len(cfg.TagPreferences) != 3 {
					t.Errorf("expected 3 tag preferences, got %d", len(cfg.TagPreferences))
				}
				if len(cfg.TagPreferences["flagship"]) != 2 {
					t.Errorf("expected 2 flagship preferences, got %d", len(cfg.TagPreferences["flagship"]))
				}
			},
		},
		{
			name: "invalid YAML syntax",
			configYAML: `
tag_preferences:
  flagship: [invalid yaml
`,
			wantErr:          true,
			wantErrSubstring: "failed to parse",
		},
		{
			name: "invalid YAML structure - wrong type",
			configYAML: `
tag_preferences: "not a map"
`,
			wantErr:          true,
			wantErrSubstring: "cannot unmarshal",
		},
		{
			name: "empty config is valid",
			configYAML: `
`,
			wantErr: false,
			validate: func(t *testing.T, cfg *UserModelsConfig) {
				if len(cfg.TagPreferences) != 0 {
					t.Errorf("expected 0 tag preferences, got %d", len(cfg.TagPreferences))
				}
				if len(cfg.Custom) != 0 {
					t.Errorf("expected 0 custom models, got %d", len(cfg.Custom))
				}
			},
		},
		{
			name: "custom model with driver_settings",
			configYAML: `
custom:
  - id: custom-o1
    name: Custom O1
    tags: [reasoning]
    providers:
      - driver: openai
        api_model: o1-custom
    driver_settings:
      preferred_endpoint: responses
      temperature_mode: omit
`,
			wantErr: false,
			validate: func(t *testing.T, cfg *UserModelsConfig) {
				if len(cfg.Custom) != 1 {
					t.Fatalf("expected 1 custom model, got %d", len(cfg.Custom))
				}
				if cfg.Custom[0].DriverSettings == nil {
					t.Fatal("expected non-nil DriverSettings")
				}
				if cfg.Custom[0].DriverSettings.PreferredEndpoint != "responses" {
					t.Errorf("PreferredEndpoint = %q, want 'responses'", cfg.Custom[0].DriverSettings.PreferredEndpoint)
				}
				if cfg.Custom[0].DriverSettings.TemperatureMode != "omit" {
					t.Errorf("TemperatureMode = %q, want 'omit'", cfg.Custom[0].DriverSettings.TemperatureMode)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with config
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "models.yaml")

			if err := os.WriteFile(configPath, []byte(tt.configYAML), 0644); err != nil {
				t.Fatalf("failed to write test config: %v", err)
			}

			// Load the config
			cfg, err := LoadUserModelsConfig(configPath)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if tt.wantErrSubstring != "" && !containsSubstring(err.Error(), tt.wantErrSubstring) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSubstring)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg == nil {
				t.Fatal("expected non-nil config")
			}

			if tt.validate != nil {
				tt.validate(t, cfg)
			}
		})
	}
}

func TestLoadUserModelsConfig_FilePermissionError(t *testing.T) {
	// Skip on Windows where permission handling is different
	if os.Getenv("GOOS") == "windows" {
		t.Skip("skipping permission test on Windows")
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "models.yaml")

	// Create file with no read permissions
	if err := os.WriteFile(configPath, []byte("test: value"), 0000); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Try to load - should fail
	_, err := LoadUserModelsConfig(configPath)
	if err == nil {
		// Clean up permissions before failing so test cleanup works
		os.Chmod(configPath, 0644)
		t.Error("expected permission error, got nil")
	}

	// Clean up - restore permissions so TempDir cleanup works
	os.Chmod(configPath, 0644)
}

func TestLoadUserModelsConfig_EndToEndIntegration(t *testing.T) {
	// Full integration test: create config file, load it, apply to registry, resolve models
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "models.yaml")

	// Get the base registry to find real model IDs for tag preferences
	baseReg, err := GetRegistry()
	if err != nil {
		t.Fatalf("failed to get base registry: %v", err)
	}

	// Get real flagship model IDs
	flagshipModels := baseReg.GetModelsByTag("flagship")
	if len(flagshipModels) < 2 {
		t.Skip("need at least 2 flagship models for this test")
	}
	firstFlagshipID := flagshipModels[0].ID
	secondFlagshipID := flagshipModels[1].ID

	// Create config that reorders flagship models (put second before first)
	configYAML := fmt.Sprintf(`
tag_preferences:
  flagship:
    - %s
    - %s
custom:
  - id: test-local-model
    name: Test Local Model
    tags: [local, fast, cheap]
    visibility: user
    capabilities:
      max_context_window: 32000
      max_output_tokens: 4096
      supports_tools: false
      supports_streaming: true
    cost:
      input_per_1m: 0.0
      output_per_1m: 0.0
    providers:
      - driver: local
        api_model: test:latest
`, secondFlagshipID, firstFlagshipID)

	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	// Load config
	cfg, err := LoadUserModelsConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Create registry with user config
	reg, err := CreateRegistryWithUserConfig(cfg)
	if err != nil {
		t.Fatalf("failed to create registry: %v", err)
	}

	// Verify custom model was added
	model, ok := reg.GetDefinition("test-local-model")
	if !ok {
		t.Fatal("custom model not found in registry")
	}
	if model.Name != "Test Local Model" {
		t.Errorf("custom model name = %q, want 'Test Local Model'", model.Name)
	}

	// Verify custom model is in tag indices
	localModels := reg.GetModelsByTag("local")
	found := false
	for _, m := range localModels {
		if m.ID == "test-local-model" {
			found = true
			break
		}
	}
	if !found {
		t.Error("custom model not found in 'local' tag index")
	}

	// Verify tag preferences were applied (second flagship should now be first)
	newFlagshipModels := reg.GetModelsByTag("flagship")
	if len(newFlagshipModels) > 0 && newFlagshipModels[0].ID != secondFlagshipID {
		t.Errorf("first flagship = %q, want %q (from tag preference)", newFlagshipModels[0].ID, secondFlagshipID)
	}

	// Verify resolution works with custom model
	resolved, err := reg.Resolve(ModelSelector{ID: "test-local-model"}, []string{"local"})
	if err != nil {
		t.Fatalf("failed to resolve custom model: %v", err)
	}
	if resolved.Provider.Driver != "local" {
		t.Errorf("resolved provider = %q, want 'local'", resolved.Provider.Driver)
	}
	if resolved.Provider.APIModel != "test:latest" {
		t.Errorf("resolved API model = %q, want 'test:latest'", resolved.Provider.APIModel)
	}
}

// =============================================================================
// Visibility Filtering Tests
// =============================================================================

func TestVisibilityDev_ExcludedFromUserFacingTag(t *testing.T) {
	reg := createTestRegistry(t)

	// Verify dev model exists in registry
	devModel, ok := reg.GetDefinition("ollama-qwen")
	if !ok {
		t.Fatal("dev model 'ollama-qwen' not found in registry")
	}
	if devModel.Visibility != VisibilityDev {
		t.Fatalf("ollama-qwen visibility = %q, want 'dev'", devModel.Visibility)
	}

	// GetUserVisibleModels should NOT include dev models (they have visibility=dev)
	userVisibleModels := reg.GetUserVisibleModels()
	for _, model := range userVisibleModels {
		if model.ID == "ollama-qwen" {
			t.Error("GetUserVisibleModels() returned dev model 'ollama-qwen'")
		}
	}
}

func TestMetaTag_ForInternalOperations(t *testing.T) {
	reg := createTestRegistry(t)

	// GetModelsByTag("meta") returns models suitable for internal operations
	metaModels := reg.GetModelsByTag(TagMeta)

	// gpt-4o-mini has the meta tag
	found := false
	for _, model := range metaModels {
		if model.ID == "gpt-4o-mini" {
			found = true
			break
		}
	}
	if !found {
		t.Error("GetModelsByTag('meta') should include 'gpt-4o-mini' which has the meta tag")
	}

	// Dev models don't have the meta tag (ollama-qwen has [local, fast, cheap])
	for _, model := range metaModels {
		if model.ID == "ollama-qwen" {
			t.Error("GetModelsByTag('meta') should NOT include dev model 'ollama-qwen'")
		}
	}
}

func TestVisibilityDev_StillAccessibleDirectly(t *testing.T) {
	reg := createTestRegistry(t)

	// GetDefinition should still find dev models
	model, ok := reg.GetDefinition("ollama-qwen")
	if !ok {
		t.Error("GetDefinition failed to find dev model 'ollama-qwen'")
	}
	if model.Visibility != VisibilityDev {
		t.Errorf("dev model visibility = %q, want 'dev'", model.Visibility)
	}

	// Resolve should still work with dev models by ID
	resolved, err := reg.Resolve(ModelSelector{ID: "ollama-qwen"}, []string{"local"})
	if err != nil {
		t.Fatalf("failed to resolve dev model: %v", err)
	}
	if resolved.Definition.ID != "ollama-qwen" {
		t.Errorf("resolved model ID = %q, want 'ollama-qwen'", resolved.Definition.ID)
	}

	// GetModelsByTag should still return dev models
	localModels := reg.GetModelsByTag("local")
	found := false
	for _, m := range localModels {
		if m.ID == "ollama-qwen" {
			found = true
			break
		}
	}
	if !found {
		t.Error("GetModelsByTag('local') did not return dev model 'ollama-qwen'")
	}
}

func TestVisibilityFiltering_Comprehensive(t *testing.T) {
	// Create a registry with models demonstrating visibility-based filtering
	yaml := `
models:
  - id: user-model
    name: User Model
    tags: [test]
    visibility: user
    providers:
      - driver: openai
        api_model: user
  - id: meta-model
    name: Meta Model
    tags: [test, meta]
    visibility: meta
    providers:
      - driver: openai
        api_model: meta
  - id: dev-model
    name: Dev Model
    tags: [test]
    visibility: dev
    providers:
      - driver: local
        api_model: dev
`

	reg, err := ParseRegistryFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to parse YAML: %v", err)
	}

	t.Run("GetUserVisibleModels includes only user-visible models", func(t *testing.T) {
		models := reg.GetUserVisibleModels()
		if len(models) != 1 {
			t.Errorf("expected 1 user-visible model, got %d", len(models))
		}
		if len(models) > 0 && models[0].ID != "user-model" {
			t.Errorf("expected user-model, got %s", models[0].ID)
		}
	})

	t.Run("GetModelsByTag(meta) returns meta operation models", func(t *testing.T) {
		models := reg.GetModelsByTag(TagMeta)
		if len(models) != 1 {
			t.Errorf("expected 1 meta model, got %d", len(models))
		}
		if len(models) > 0 && models[0].ID != "meta-model" {
			t.Errorf("expected meta-model, got %s", models[0].ID)
		}
	})

	t.Run("ListAll includes everything", func(t *testing.T) {
		models := reg.ListAll()
		if len(models) != 3 {
			t.Errorf("expected 3 models, got %d", len(models))
		}
		modelIDs := make(map[string]bool)
		for _, m := range models {
			modelIDs[m.ID] = true
		}
		for _, wantID := range []string{"user-model", "meta-model", "dev-model"} {
			if !modelIDs[wantID] {
				t.Errorf("expected model %q to be included", wantID)
			}
		}
	})

	t.Run("GetModelsByTag(test) returns all models with test tag", func(t *testing.T) {
		models := reg.GetModelsByTag("test")
		if len(models) != 3 {
			t.Errorf("expected 3 models with test tag, got %d", len(models))
		}
	})
}
