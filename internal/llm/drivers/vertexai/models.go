// Copyright (c) 2025 Reliant Labs
package vertexai

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for VertexAI
const Family models.Family = "vertexai"

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Serves whatever models.yaml maps to `vertexai` — both the vertex-* Model
	// Garden entries and the plain claude-*/gemini-* models that carry a
	// vertexai provider mapping.
	models.RegisterCatalogDriver(Family)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}
