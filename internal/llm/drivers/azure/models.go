// Copyright (c) 2025 Reliant Labs
package azure

import "github.com/reliant-labs/reliant/internal/llm/models"

const Family models.Family = "azure"

// SupportedModels lists the models that the Azure driver supports
// Azure supports a curated subset of OpenAI models available via Azure deployments
var SupportedModels = []models.ModelID{
	models.GPT52,
	models.GPT54Mini,
	models.GPT52Pro,
	models.GPT52Codex,
}

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, SupportedModels)
}
