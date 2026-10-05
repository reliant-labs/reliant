package copilot

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Every model the catalog maps to copilot must be servable by the driver. A
// hard-coded allowlist once rejected gemini-3.8-flash, so an explicit @copilot
// selection silently fell back to another provider.
func TestCopilotServesEveryCatalogMapping(t *testing.T) {
	registry := models.MustGetRegistry()
	mapped := registry.ListModelsByProvider("copilot")
	if len(mapped) == 0 {
		t.Fatal("catalog maps no models to copilot")
	}
	for _, def := range mapped {
		if !models.CanDriverUseModel(Family, models.ModelID(def.ID)) {
			t.Errorf("catalog maps %s to copilot but CanDriverUseModel is false", def.ID)
		}
	}
	if !models.CanDriverUseModel(Family, models.Gemini38Flash) {
		t.Error("copilot must serve gemini-3.8-flash")
	}
}
