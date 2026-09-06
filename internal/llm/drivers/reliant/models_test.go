package reliant

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/pkg/llmcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportedModelsMatchRegisteredDriverAllowlist(t *testing.T) {
	assert.ElementsMatch(t, SupportedModels, models.GetModelsForDriver(Family))
}

// TestSupportedModelsMatchGatewayCatalog pins the derivation to the public
// roster control-plane generates its allowlist from. SupportedModels is
// computed from GatewayModelIDs today, so this cannot drift by editing one
// list — but it fails loudly if anyone reintroduces a hand-written copy here.
func TestSupportedModelsMatchGatewayCatalog(t *testing.T) {
	ids := llmcatalog.GatewayModelIDs()
	expected := make([]models.ModelID, 0, len(ids))
	for _, id := range ids {
		expected = append(expected, models.ModelID(id))
	}
	assert.Equal(t, expected, SupportedModels)
}

// TestSupportedModelsAreServableThroughTheGateway checks the roster against
// models.yaml from this side too: every model the driver claims to support
// must actually carry a `reliant` provider mapping, or requests for it fail
// at resolution time.
func TestSupportedModelsAreServableThroughTheGateway(t *testing.T) {
	registry, err := models.ParseRegistry()
	require.NoError(t, err)

	servable := make(map[string]bool)
	for _, definition := range registry.ListModelsByProvider(string(Family)) {
		servable[definition.ID] = true
	}

	for _, id := range SupportedModels {
		assert.True(t, servable[string(id)],
			"model %q is in SupportedModels but has no reliant provider mapping in models.yaml", id)
	}
}

func TestSupportedModelsExcludeNonReliantVertexVariants(t *testing.T) {
	assert.NotContains(t, SupportedModels, models.VertexClaude45Sonnet)
	assert.NotContains(t, SupportedModels, models.VertexClaude46Opus)
	assert.NotContains(t, SupportedModels, models.VertexGemini25Pro)
	assert.NotContains(t, SupportedModels, models.VertexGemini25Flash)
}

func TestSupportedModelsExposeSupportedClaudeModels(t *testing.T) {
	assert.Contains(t, SupportedModels, models.Claude46Opus)
	assert.Contains(t, SupportedModels, models.Claude45Opus)
	assert.Contains(t, SupportedModels, models.Claude46Sonnet)
	assert.Contains(t, SupportedModels, models.Claude45Sonnet)
	assert.Contains(t, SupportedModels, models.Claude45Haiku)
}

func TestSupportedModelsOnlyExposeCodingFocusedGeminiModels(t *testing.T) {
	assert.Contains(t, SupportedModels, models.Gemini31ProPreview)
	assert.Contains(t, SupportedModels, models.Gemini3FlashPreview)
	assert.Contains(t, SupportedModels, models.Gemini25Pro)
	assert.NotContains(t, SupportedModels, models.Gemini3ProPreview)
	assert.NotContains(t, SupportedModels, models.Gemini31FlashLitePreview)
	assert.NotContains(t, SupportedModels, models.Gemini25Flash)
	assert.NotContains(t, SupportedModels, models.Gemini25FlashLite)
}
