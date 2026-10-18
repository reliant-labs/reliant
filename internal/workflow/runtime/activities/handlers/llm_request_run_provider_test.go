// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The provider a person chose for a run (RuntimeContext.ProviderPreference →
// llmCallSpec.RunProviders) decides where a tier selector with no provider of
// its own resolves. Both Claude and Codex are connected throughout: the tier
// lists lead with Claude, which is the whole bug.

func resolveWithBothConnected(t *testing.T, spec llmCallSpec) (*resolvedLLMCall, error) {
	t.Helper()
	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	provisionProviderKeys(t, ctx, userID, []string{"anthropic", "codex"})
	captured := &capturedDriverOptions{}
	original := drivers.GetDriver
	drivers.GetDriver = captureDriverOptionsResolver(captured)
	t.Cleanup(func() { drivers.GetDriver = original })
	spec.UserID, spec.SessionID = userID, "s"
	return resolveLLMCall(ctx, nil, spec)
}

func TestResolveLLMCall_RunProviderMovesATierClaudeLeads(t *testing.T) {
	fast := models.ModelSelector{Tags: []string{models.TagFast}}

	t.Run("without a run provider the tier's leader wins", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: fast})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})

	t.Run("a codex run resolves its sub-agent's tier on codex", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: fast, RunProviders: []string{"codex"}})
		require.NoError(t, err)
		assert.Equal(t, "codex", res.ProviderDriver)
	})

	t.Run("a selector's own pin is never overridden", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{
			Selector:     models.ModelSelector{ID: "claude-4.5-haiku@anthropic"},
			RunProviders: []string{"codex"},
		})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
		assert.Equal(t, "claude-4.5-haiku@anthropic", res.ModelID)
	})

	t.Run("a selector's own providers list is never overridden", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{
			Selector:     models.ModelSelector{Tags: []string{models.TagFast}, Providers: []string{"anthropic"}},
			RunProviders: []string{"codex"},
		})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})

	t.Run("a run provider that serves nothing in the tier falls back to the tier's order", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: fast, RunProviders: []string{"gemini"}})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})
}

// The user's per-tag model preference can pin a tier to a model the run's
// provider cannot serve. The run's provider is the more specific fact: the
// preference's model is skipped, its thinking level and temperature are kept.
func TestResolveLLMCall_RunProviderBeatsTagPreferenceModel(t *testing.T) {
	fast := models.ModelSelector{Tags: []string{models.TagFast}}
	reader := fakeSettingsReader{rows: map[string]string{
		"model.tag_config.fast": `{"model_id":"claude-4.5-haiku","thinking_level":"low","temperature":0.4}`,
	}}

	t.Run("without a run provider the tag preference's model applies", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: fast, TagPrefsReader: reader})
		require.NoError(t, err)
		assert.Equal(t, "claude-4.5-haiku@anthropic", res.ModelID)
	})

	t.Run("a codex run skips a model codex cannot serve but keeps thinking and temperature", func(t *testing.T) {
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: fast, TagPrefsReader: reader, RunProviders: []string{"codex"}})
		require.NoError(t, err)
		assert.Equal(t, "codex", res.ProviderDriver)
		assert.NotEqual(t, "claude-4.5-haiku@anthropic", res.ModelID)
		assert.Equal(t, "low", res.ThinkingLevel)
		require.NotNil(t, res.Temperature)
		assert.InDelta(t, 0.4, *res.Temperature, 1e-9)
	})

	t.Run("a tag preference the run's provider serves still applies", func(t *testing.T) {
		pref := fakeSettingsReader{rows: map[string]string{
			"model.tag_config.fast": `{"model_id":"gpt-6-luna"}`,
		}}
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: fast, TagPrefsReader: pref, RunProviders: []string{"codex"}})
		require.NoError(t, err)
		assert.Equal(t, "gpt-6-luna@codex", res.ModelID)
	})
}

func TestWithRunProviders(t *testing.T) {
	tier := models.ModelSelector{Tags: []string{"fast"}}
	got, applied := withRunProviders(tier, []string{"codex"})
	assert.True(t, applied)
	assert.Equal(t, []string{"codex"}, got.Providers)
	assert.Empty(t, tier.Providers, "the caller's selector is not mutated")

	for name, sel := range map[string]models.ModelSelector{
		"pinned id":     {ID: "m@anthropic"},
		"own providers": {Tags: []string{"fast"}, Providers: []string{"openai"}},
	} {
		got, applied := withRunProviders(sel, []string{"codex"})
		assert.False(t, applied, name)
		assert.Equal(t, sel, got, name)
	}
	_, applied = withRunProviders(tier, nil)
	assert.False(t, applied)
}
