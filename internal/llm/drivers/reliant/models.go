package reliant

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const Family models.Family = "reliant"

// SupportedModels is the curated Reliant-backed allowlist exposed in the app.
// Keep this aligned with the control-plane allowlist used for key provisioning.
var SupportedModels = []models.ModelID{
	models.Claude5Opus,
	models.Claude46Opus,
	models.Claude45Opus,
	models.Claude46Sonnet,
	models.Claude45Sonnet,
	models.Claude45Haiku,
	models.Gemini31ProPreview,
	models.Gemini3FlashPreview,
	models.Gemini25Pro,
}

func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts), nil
}

func init() {
	models.RegisterDriverModels(Family, SupportedModels)
	registry.RegisterDriver(Family, createClient)
}
