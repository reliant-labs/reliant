// Copyright (c) 2025 Reliant Labs
package xai

import "github.com/reliant-labs/reliant/internal/llm/models"

// Family constant for XAI
const Family models.Family = "xai"

// SupportedModels lists the models that the XAI driver supports
var SupportedModels = []models.ModelID{
	models.Grok4,
	models.GrokCodeFast,
	models.Grok3Beta,
	models.Grok3MiniBeta,
	models.Grok3FastBeta,
	models.Grok3MiniFastBeta,
}

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, SupportedModels)
}
