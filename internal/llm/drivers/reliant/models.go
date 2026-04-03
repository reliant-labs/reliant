package reliant

import (
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

func init() {
	reg := models.MustGetRegistry()
	allModels := reg.ListAll()
	ids := make([]models.ModelID, 0, len(allModels))
	for _, def := range allModels {
		ids = append(ids, models.ModelID(def.ID))
	}
	models.RegisterDriverModels(Family, ids)
	registry.RegisterDriver(Family, createClient)
}
