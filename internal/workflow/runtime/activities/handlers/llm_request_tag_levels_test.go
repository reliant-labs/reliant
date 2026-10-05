// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The registry knowing about per-entry tag thinking levels proves nothing on
// its own — resolveLLMCall is what turns a resolution into a request, and it
// owns the precedence between an explicit level and the resolved level (the
// winning tag entry's, or the model's capability default for id selection).
// This exercises the real registry path (no injected resolver, which skips
// registry resolution entirely).
func TestResolveLLMCall_AppliesTagThinkingLevel(t *testing.T) {
	tests := []struct {
		name string
		// selector picks the model; thinkingLevel is the explicit per-call
		// override, empty meaning "unset" as an absent workflow arg would be.
		selector      models.ModelSelector
		thinkingLevel string
		providers     []string
		wantModelID   string
		wantThinking  string
	}{
		{
			name:         "tag entry level applies when no explicit level is given",
			selector:     models.ModelSelector{Tags: []string{models.TagPowerful}},
			providers:    []string{"anthropic"},
			wantModelID:  "claude-5.1-fable@anthropic",
			wantThinking: "xhigh",
		},
		{
			name:          "explicit level beats the tag entry level",
			selector:      models.ModelSelector{Tags: []string{models.TagPowerful}},
			thinkingLevel: "low",
			providers:     []string{"anthropic"},
			wantModelID:   "claude-5.1-fable@anthropic",
			wantThinking:  "low",
		},
		{
			name:      "tag entry level clamps to a model that tops out below it",
			selector:  models.ModelSelector{Tags: []string{models.TagPowerful}},
			providers: []string{"gemini"},
			// gemini-3.8-flash is powerful but supports only low/medium/high.
			wantModelID:  "gemini-3.8-flash@gemini",
			wantThinking: "high",
		},
		{
			name:        "selection by id ignores tag levels and uses the capability default",
			selector:    models.ModelSelector{ID: "claude-5.1-fable"},
			providers:   []string{"anthropic"},
			wantModelID: "claude-5.1-fable@anthropic",
			// No tag did the selecting, so no tag entry's level applies. The
			// model carries no effort of its own any more; its capability
			// default is medium because it supports medium.
			wantThinking: "medium",
		},
		{
			name:      "flagship on copilot runs claude-5.5-sonnet at high",
			selector:  models.ModelSelector{Tags: []string{models.TagFlagship}},
			providers: []string{"copilot"},
			// Copilot's individual plan reports the opus entries policy=disabled,
			// so flagship falls through to the first sonnet it serves —
			// claude-5.5-sonnet (enabled, mapped 2026-10-04) — at its level.
			wantModelID:  "claude-5.5-sonnet@copilot",
			wantThinking: "high",
		},
		{
			// The general preset: [flagship] → 5.5 at the flagship entry's xhigh.
			name:         "flagship tier runs claude-5.5-opus at xhigh",
			selector:     models.ModelSelector{Tags: []string{models.TagFlagship}},
			providers:    []string{"anthropic"},
			wantModelID:  "claude-5.5-opus@anthropic",
			wantThinking: "xhigh",
		},
		{
			// The implementer preset: [moderate] → claude-5.5-sonnet at medium.
			name:         "moderate tier runs claude-5.5-sonnet at medium",
			selector:     models.ModelSelector{Tags: []string{models.TagModerate}},
			providers:    []string{"anthropic"},
			wantModelID:  "claude-5.5-sonnet@anthropic",
			wantThinking: "medium",
		},
		{
			name:          "an explicit level still beats the tier effort",
			selector:      models.ModelSelector{Tags: []string{models.TagModerate}},
			thinkingLevel: "low",
			providers:     []string{"anthropic"},
			wantModelID:   "claude-5.5-sonnet@anthropic",
			wantThinking:  "low",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID := "user-" + uuid.NewString()
			ctx := context.Background()
			provisionProviderKeys(t, ctx, userID, tt.providers)

			// Stub driver construction: this test is about the settings
			// resolveLLMCall computes, not about reaching a provider.
			captured := &capturedDriverOptions{}
			original := drivers.GetDriver
			drivers.GetDriver = captureDriverOptionsResolver(captured)
			t.Cleanup(func() { drivers.GetDriver = original })

			resolved, err := resolveLLMCall(ctx, nil, llmCallSpec{
				UserID:        userID,
				SessionID:     "session-" + uuid.NewString(),
				Selector:      tt.selector,
				ThinkingLevel: tt.thinkingLevel,
			})
			require.NoError(t, err)

			assert.Equal(t, tt.wantModelID, resolved.ModelID)
			assert.Equal(t, tt.wantThinking, resolved.ThinkingLevel)
			// The level actually reaches the driver, not just the struct.
			assert.Equal(t, tt.wantThinking, captured.ReasoningEffort)
		})
	}
}

// tagEntryLevelOverCapabilityDefaultCatalog is a fixture catalog whose tag
// entry level and the model's capability default DISAGREE.
//
// The shipping catalog cannot cleanly prove this precedence on its own terms,
// so here the powerful entry says xhigh while the model's capability default
// (it supports medium) is medium. Only an implementation that consults the
// winning tag entry can pass.
const tagEntryLevelOverCapabilityDefaultCatalog = `
tags:
  powerful:
    - {model: fixture-powerful, thinking_level: xhigh}
  reasoning:
    - {model: fixture-powerful}
models:
  - id: fixture-powerful
    name: Fixture Powerful
    capabilities:
      can_reason: true
      supports_tools: true
      supports_streaming: true
      max_context_window: 200000
      max_output_tokens: 8192
      thinking_levels: [low, medium, high, xhigh]
    providers:
      - driver: anthropic
        api_model: fixture-powerful
`

func TestResolveLLMCall_TagEntryLevelBeatsCapabilityDefault(t *testing.T) {
	fixtureReg, err := models.ParseRegistryFromBytes([]byte(tagEntryLevelOverCapabilityDefaultCatalog))
	require.NoError(t, err)

	fixtureModel, ok := fixtureReg.GetDefinition("fixture-powerful")
	require.True(t, ok)
	require.Equal(t, "medium", models.ThinkingLevelFor(fixtureModel),
		"fixture precondition: the capability default must differ from the tag entry's level")

	// The global registry is process-wide; no test in this package runs in
	// parallel, so swap it for the duration and put the default back after.
	previous := models.MustGetRegistry()
	models.SetGlobalRegistry(fixtureReg)
	t.Cleanup(func() { models.SetGlobalRegistry(previous) })

	userID := "user-" + uuid.NewString()
	ctx := context.Background()
	provisionProviderKeys(t, ctx, userID, []string{"anthropic"})

	captured := &capturedDriverOptions{}
	original := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(captured)
	t.Cleanup(func() { drivers.GetDriver = original })

	resolved, err := resolveLLMCall(ctx, nil, llmCallSpec{
		UserID:    userID,
		SessionID: "session-" + uuid.NewString(),
		Selector:  models.ModelSelector{Tags: []string{models.TagPowerful}},
	})
	require.NoError(t, err)

	require.Equal(t, "fixture-powerful@anthropic", resolved.ModelID)
	assert.Equal(t, "xhigh", resolved.ThinkingLevel,
		"the tag entry's level must win over the model's capability default")
	assert.Equal(t, "xhigh", captured.ReasoningEffort)

	// And the capability default applies when the model is named by id,
	// since no tag entry did the selecting.
	byID, err := resolveLLMCall(ctx, nil, llmCallSpec{
		UserID:    userID,
		SessionID: "session-" + uuid.NewString(),
		Selector:  models.ModelSelector{ID: "fixture-powerful"},
	})
	require.NoError(t, err)
	assert.Equal(t, "medium", byID.ThinkingLevel)
}

// provisionProviderKeys gives userID a configured API key for each driver, so
// registry resolution sees exactly those providers as available.
func provisionProviderKeys(t *testing.T, ctx context.Context, userID string, providers []string) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	drivers.InitializeAPIKeyProvider(repo)
	for _, provider := range providers {
		require.NoError(t, repo.SetProviderAPIKey(ctx, userID, provider, "test-key-"+provider))
	}
}
