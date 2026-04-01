// Copyright (c) 2025 Reliant Labs
package bedrock

import "github.com/reliant-labs/reliant/internal/llm/models"

// SupportedModels lists the models that the Bedrock driver supports
// Currently only supports Anthropic models through AWS Bedrock
var SupportedModels = []models.ModelID{
	models.Claude45Haiku,
	models.Claude45Sonnet,
	models.Claude46Sonnet,
	models.Claude45Opus,
	models.Claude46Opus,
}

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, SupportedModels)
}
