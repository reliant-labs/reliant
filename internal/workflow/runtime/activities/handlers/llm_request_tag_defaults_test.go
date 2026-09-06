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

// The registry knowing about tag-carried thinking defaults proves nothing on
// its own — resolveLLMCall is what turns a resolution into a request, and it
// owns the precedence between an explicit level, the tag default, and the
// model's own default. This exercises the real registry path (no injected
// resolver, which skips registry resolution entirely).
func TestResolveLLMCall_AppliesTagThinkingDefault(t *testing.T) {
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
			name:         "tag default applies when no explicit level is given",
			selector:     models.ModelSelector{Tags: []string{models.TagPowerful}},
			providers:    []string{"anthropic"},
			wantModelID:  "claude-5.1-fable@anthropic",
			wantThinking: "xhigh",
		},
		{
			name:          "explicit level beats the tag default",
			selector:      models.ModelSelector{Tags: []string{models.TagPowerful}},
			thinkingLevel: "low",
			providers:     []string{"anthropic"},
			wantModelID:   "claude-5.1-fable@anthropic",
			wantThinking:  "low",
		},
		{
			name:      "tag default clamps to a model that tops out below it",
			selector:  models.ModelSelector{Tags: []string{models.TagPowerful}},
			providers: []string{"gemini"},
			// gemini-3.8-flash is powerful but supports only low/medium/high.
			wantModelID:  "gemini-3.8-flash@gemini",
			wantThinking: "high",
		},
		{
			name:         "selection by id ignores the tag default and uses the model default",
			selector:     models.ModelSelector{ID: "claude-5.1-fable"},
			providers:    []string{"anthropic"},
			wantModelID:  "claude-5.1-fable@anthropic",
			wantThinking: "xhigh", // the model's own default_thinking_level
		},
		{
			name:      "a tag with no default falls back to the model default",
			selector:  models.ModelSelector{Tags: []string{models.TagFlagship}},
			providers: []string{"anthropic"},
			// flagship declares no tag default, so claude-5-opus's own
			// default_thinking_level (high) is what survives.
			wantModelID:  "claude-5-opus@anthropic",
			wantThinking: "high",
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

// tagDefaultOverModelDefaultCatalog is a fixture catalog whose tag default and
// per-model default DISAGREE.
//
// The shipping catalog cannot prove this precedence: every powerful model's own
// default_thinking_level already equals the clamped tag default, so a handler
// that ignored the tag entirely would still produce the right answer. Here the
// tag says xhigh and the model says low, so only an implementation that
// consults the tag can pass.
const tagDefaultOverModelDefaultCatalog = `
tag_defaults:
  powerful:
    thinking_level: xhigh
models:
  - id: fixture-powerful
    name: Fixture Powerful
    tags: [powerful, reasoning]
    capabilities:
      can_reason: true
      supports_tools: true
      supports_streaming: true
      max_context_window: 200000
      max_output_tokens: 8192
      thinking_levels: [low, medium, high, xhigh]
    default_thinking_level: low
    providers:
      - driver: anthropic
        api_model: fixture-powerful
`

func TestResolveLLMCall_TagDefaultBeatsModelDefault(t *testing.T) {
	fixtureReg, err := models.ParseRegistryFromBytes([]byte(tagDefaultOverModelDefaultCatalog))
	require.NoError(t, err)

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
		"the tag default must win over the model's own default_thinking_level")
	assert.Equal(t, "xhigh", captured.ReasoningEffort)

	// And the model default still wins when the model is named by id, since
	// no tag did the selecting.
	byID, err := resolveLLMCall(ctx, nil, llmCallSpec{
		UserID:    userID,
		SessionID: "session-" + uuid.NewString(),
		Selector:  models.ModelSelector{ID: "fixture-powerful"},
	})
	require.NoError(t, err)
	assert.Equal(t, "low", byID.ThinkingLevel)
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
