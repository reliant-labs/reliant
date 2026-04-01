// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	"github.com/reliant-labs/reliant/internal/llm/drivers/gemini"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for Copilot
const Family models.Family = "copilot"

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, anthropic.SupportedModels)
	models.RegisterDriverModels(Family, gemini.SupportedModels)
	models.RegisterDriverModels(Family, openai.SupportedModels)
}
