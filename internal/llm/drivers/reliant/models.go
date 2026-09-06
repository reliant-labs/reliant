package reliant

import (
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/pkg/llmcatalog"
)

const Family models.Family = "reliant"

// SupportedModels is the curated Reliant-backed allowlist exposed in the app.
//
// It DERIVES from pkg/llmcatalog rather than restating the list, because the
// control-plane artifacts that provision gateway keys generate themselves from
// that same roster. The two lists having drifted apart is what let production
// serve a model set four entries behind the repo. Add a model to the roster in
// pkg/llmcatalog and both sides move together.
//
// GatewayModelIDs reads no files and cannot fail, so it is safe in a
// package-level initializer: Go fully initializes pkg/llmcatalog before this
// package's variables, and before the init() below registers them.
var SupportedModels = gatewaySupportedModels()

func gatewaySupportedModels() []models.ModelID {
	ids := llmcatalog.GatewayModelIDs()
	supported := make([]models.ModelID, 0, len(ids))
	for _, id := range ids {
		supported = append(supported, models.ModelID(id))
	}
	return supported
}

func createClient(opts *llm.DriverOptions) (registry.Client, error) {
	return NewClient(*opts), nil
}

func init() {
	models.RegisterDriverModels(Family, SupportedModels)
	registry.RegisterDriver(Family, createClient)
}
