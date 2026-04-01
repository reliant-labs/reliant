// Copyright (c) 2025 Reliant Labs
package groq

import "github.com/reliant-labs/reliant/internal/llm/models"

// Family constant for Groq
const Family models.Family = "groq"

// SupportedModels lists the models that the Groq driver supports
// Note: Groq models not yet added to models.yaml - add them there when needed
var SupportedModels = []models.ModelID{}

func init() {
	// Register which models this driver supports
	models.RegisterDriverModels(Family, SupportedModels)
}
