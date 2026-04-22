package reliant

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/stretchr/testify/assert"
)

func TestSupportedModelsMatchRegisteredDriverAllowlist(t *testing.T) {
	assert.ElementsMatch(t, SupportedModels, models.GetModelsForDriver(Family))
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
