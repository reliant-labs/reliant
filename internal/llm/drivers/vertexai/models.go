// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for VertexAI
const Family models.Family = "vertexai"

// SupportedModels lists the models that the VertexAI driver supports
// VertexAI supports multiple providers through Model Garden
var SupportedModels = []models.ModelID{
	// Gemini models
	models.VertexGemini25Pro,
	models.VertexGemini25Flash,

	// Claude models via Model Garden
	models.VertexClaude45Sonnet,
	models.VertexClaude46Opus,
	models.VertexClaude45Haiku,
	models.VertexClaude48Opus,
	models.VertexClaude5Sonnet,
	models.VertexClaude5Fable,
}

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, SupportedModels)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}
