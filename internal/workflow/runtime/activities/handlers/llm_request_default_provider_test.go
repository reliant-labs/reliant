// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// The user's Settings default provider (model.provider_preference) decides
// which connected provider a tier runs on. Claude and Codex are connected
// throughout, and the tier lists lead with Claude: without the setting every
// auto tier went to Claude and the user had no say.
func TestResolveLLMCall_DefaultProviderChoosesTheTiersProvider(t *testing.T) {
	t.Parallel()
	flagship := models.ModelSelector{Tags: []string{models.TagFlagship}}
	codexFirst := fakeSettingsReader{rows: map[string]string{
		"model.provider_preference": `{"providers":["codex"]}`,
	}}

	t.Run("Auto keeps the tier's order", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: flagship, TagPrefsReader: fakeSettingsReader{}})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})

	t.Run("a chat turn resolves the tier on the default provider at its entry's effort", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: flagship, TagPrefsReader: codexFirst})
		require.NoError(t, err)
		assert.Equal(t, "gpt-6.2-sol@codex", res.ModelID)
		assert.Equal(t, "high", res.ThinkingLevel)
	})

	t.Run("housekeeping calls apply it without tag preferences", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{
			Selector:            titleModelSelector(),
			FallbackSelector:    titleModelFallbackSelector(),
			ProviderPrefsReader: codexFirst,
		})
		require.NoError(t, err)
		assert.Equal(t, "codex", res.ProviderDriver)
	})

	t.Run("the run's own provider outranks it", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: flagship, TagPrefsReader: codexFirst, RunProviders: []string{"anthropic"}})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})

	t.Run("a tier pinned to a model keeps it", func(t *testing.T) {
		t.Parallel()
		reader := fakeSettingsReader{rows: map[string]string{
			"model.provider_preference": `{"providers":["codex"]}`,
			"model.tag_config.flagship": `{"model_id":"claude-5.5-opus"}`,
		}}
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: flagship, TagPrefsReader: reader})
		require.NoError(t, err)
		assert.Equal(t, "claude-5.5-opus@anthropic", res.ModelID)
	})

	t.Run("a selector naming its provider keeps it", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: models.ModelSelector{ID: "claude-4.5-haiku@anthropic"}, TagPrefsReader: codexFirst})
		require.NoError(t, err)
		assert.Equal(t, "claude-4.5-haiku@anthropic", res.ModelID)
	})

	t.Run("a default provider that is not connected falls back to the tier's order", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: flagship, TagPrefsReader: fakeSettingsReader{rows: map[string]string{
			"model.provider_preference": `{"providers":["gemini"]}`,
		}}})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})

	t.Run("an unreadable setting resolves as Auto", func(t *testing.T) {
		t.Parallel()
		res, err := resolveWithBothConnected(t, llmCallSpec{Selector: flagship, TagPrefsReader: fakeSettingsReader{rows: map[string]string{
			"model.provider_preference": `{oops`,
		}}})
		require.NoError(t, err)
		assert.Equal(t, "anthropic", res.ProviderDriver)
	})
}
