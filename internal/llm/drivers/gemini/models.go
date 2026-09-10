// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for Gemini
const Family models.Family = "gemini"

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Serves whatever models.yaml maps to `gemini`.
	models.RegisterCatalogDriver(Family)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}
