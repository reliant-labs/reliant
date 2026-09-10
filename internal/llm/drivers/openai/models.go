// Copyright (c) 2025 Reliant Labs
package openai

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const Family models.Family = "openai"

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts), nil
}

func init() {
	// Serves whatever models.yaml maps to `openai`.
	models.RegisterCatalogDriver(Family)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}
