// Copyright (c) 2025 Reliant Labs
package openai

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const Family models.Family = "openai"

// SupportedModels lists the models that the OpenAI driver supports
var SupportedModels = []models.ModelID{
	// GPT-5.2 / GPT-5.3 / GPT-5.4 / GPT-5.5 family
	models.GPT55,
	models.GPT54,
	models.GPT54Mini,
	models.GPT54Pro,
	models.GPT52,
	models.GPT52Pro,
	models.GPT53Codex, // Flagship Codex model on OpenAI
	models.GPT52Codex, // Keep previous Codex generation available
}

// createClient is the driver factory function for the registry
func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts), nil
}

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, SupportedModels)
	// Register the driver factory
	registry.RegisterDriver(Family, createClient)
}