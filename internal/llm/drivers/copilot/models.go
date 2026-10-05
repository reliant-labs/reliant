// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for Copilot
const Family models.Family = "copilot"

// Copilot serves exactly the models whose `copilot` provider entry exists in
// models.yaml (api_model there is the dotted id Copilot's HTTP API expects). A
// separate hard-coded allowlist here disagreed with the catalog: any mapped model
// missing from it failed CanDriverUseModel, so an explicit @copilot selection
// silently fell back to another driver.

// createClient is the driver factory function for the registry.
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts)
}

func init() {
	// Serve whatever the catalog maps to copilot.
	models.RegisterCatalogDriver(Family)
	// Register the driver factory so drivers.GetDriverForModel can construct it.
	registry.RegisterDriver(Family, createClient)
}
