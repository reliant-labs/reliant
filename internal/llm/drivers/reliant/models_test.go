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
