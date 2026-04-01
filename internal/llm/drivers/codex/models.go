// Copyright (c) 2025 Reliant Labs
package codex

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family identifies this driver in the registry
const Family models.Family = "codex"

// SupportedModels lists the models that the Codex driver supports
var SupportedModels = []models.ModelID{
	models.GPT54,
	models.GPT54Mini,
	models.GPT52Codex,
	models.GPT53Codex,
	models.GPT53CodexSpark,
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
