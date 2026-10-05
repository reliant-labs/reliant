// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/modelprefs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTierResolutions_ReflectTagPreferences(t *testing.T) {
	registry := models.MustGetRegistry()
	providers := []string{"anthropic"}

	t.Run("model_id and thinking_level override the tier", func(t *testing.T) {
		prefs := map[string]modelprefs.TagPrefs{
			"moderate": {ModelID: "claude-5.5-opus", ThinkingLevel: "low"},
		}
		tier, ok := tiersByTag(tierResolutions(registry, providers, prefs, nil))["moderate"]
		require.True(t, ok)
		assert.Equal(t, "claude-5.5-opus@anthropic", tier.ModelId)
		assert.Equal(t, "low", tier.ThinkingLevel)
	})

	t.Run("thinking_level alone keeps the tier's model", func(t *testing.T) {
		prefs := map[string]modelprefs.TagPrefs{"moderate": {ThinkingLevel: "high"}}
		tier := tiersByTag(tierResolutions(registry, providers, prefs, nil))["moderate"]
		assert.Equal(t, "claude-5.5-sonnet@anthropic", tier.ModelId)
		assert.Equal(t, "high", tier.ThinkingLevel)
	})

	t.Run("unavailable model_id falls back to the tier", func(t *testing.T) {
		prefs := map[string]modelprefs.TagPrefs{"moderate": {ModelID: "gpt-6-sol"}}
		tier := tiersByTag(tierResolutions(registry, providers, prefs, nil))["moderate"]
		assert.Equal(t, "claude-5.5-sonnet@anthropic", tier.ModelId)
		assert.Equal(t, "medium", tier.ThinkingLevel)
	})
}

func TestTierResolutions_PinnedLocalModel(t *testing.T) {
	registry := models.MustGetRegistry()
	qwen := &reliantv1.LocalModelInfo{Name: "qwen3:latest", ContextWindow: 40960, SupportsChat: true}
	inv := &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{Id: "ollama", Kind: "ollama", Models: []*reliantv1.LocalModelInfo{qwen}}}}
	prefs := map[string]modelprefs.TagPrefs{"moderate": {ModelID: "qwen3:latest@local", Providers: []string{"local:gpu"}}}

	online := local.Synthesize(local.DaemonInventory{DaemonID: "gpu", Machine: "GPU box", Online: true, Inventory: inv})
	tier := tiersByTag(tierResolutions(registry, []string{"anthropic"}, prefs, online))["moderate"]
	assert.Equal(t, "qwen3:latest@local", tier.ModelId, "tier shows the pinned local model while its machine is online")

	offline := local.Synthesize(local.DaemonInventory{DaemonID: "gpu", Machine: "GPU box", Online: false, Inventory: inv})
	tier = tiersByTag(tierResolutions(registry, []string{"anthropic"}, prefs, offline))["moderate"]
	assert.Equal(t, "claude-5.5-sonnet@anthropic", tier.ModelId, "offline machine falls back to the tier")
}
