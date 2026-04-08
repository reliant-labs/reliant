// Copyright (c) 2025 Reliant Labs
package reliant

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
)

const Family models.Family = "reliant"

func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts), nil
}

func init() {
	// Register models that have reliant as a provider from the YAML registry.
	reg := models.MustGetRegistry()
	reliantModels := reg.ListModelsByProvider("reliant")
	m := make([]models.ModelID, 0, len(reliantModels))
	for _, def := range reliantModels {
		m = append(m, models.ModelID(def.ID))
	}
	models.RegisterDriverModels(Family, m)
	registry.RegisterDriver(Family, createClient)
}
