// Copyright (c) 2025 Reliant Labs
package antigravity

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family is the driver id, and the provider name models.yaml maps models to.
const Family models.Family = "antigravity"

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Serves whatever models.yaml maps to `antigravity`.
	models.RegisterCatalogDriver(Family)
	registry.RegisterDriver(Family, createClient)
}
