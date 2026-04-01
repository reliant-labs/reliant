// Copyright (c) 2025 Reliant Labs
package gemini

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Family constant for Gemini
const Family models.Family = "gemini"

// SupportedModels lists the models that the Gemini driver supports
// Updated December 2025
var SupportedModels = []models.ModelID{
	// Gemini 3 Series (Latest)
	models.Gemini31ProPreview,
	models.Gemini31ProPreviewCustomTools,
	models.Gemini3ProPreview,
	models.Gemini3FlashPreview,

	// Gemini 2.5 Series
	models.Gemini25Pro,
	models.Gemini25Flash,
	models.Gemini25FlashLite,
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
