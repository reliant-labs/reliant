// Copyright (c) 2025 Reliant Labs
package models

import "sync"

// Which models a driver can serve has two possible sources, and the split is
// the whole point of this file.
//
// For almost every driver the answer is already written down in models.yaml:
// a `- driver: anthropic` provider mapping on a model IS the statement that
// Anthropic serves it. Those drivers call RegisterCatalogDriver and are
// resolved against the catalog directly, so there is nothing to keep in sync.
//
// A few drivers genuinely know something the catalog does not:
//
//   - reliant serves a curated subset of what it is mapped to — a mapping says
//     the gateway COULD route a model, the roster in pkg/llmcatalog says we
//     have chosen to sell it.
//   - copilot is gated on the caller's GitHub plan.
//   - local discovers its models from the user's endpoint at runtime.
//
// Those call RegisterDriverModels with an explicit list.
//
// Before this split, every driver hand-maintained a list that merely restated
// the catalog, and resolution consulted the list while the model picker
// consulted the catalog. Adding a model to models.yaml without also editing
// the matching driver produced a model that appeared in the picker and then
// failed to resolve, reporting:
//
//	model "claude-5.1-fable": no configured driver can serve it
//	(explicitDriver="anthropic")
//
// which reads as a disconnected provider and sent debugging in the wrong
// direction entirely. claude-5.1-fable, five Gemini models and both Vertex
// Fable entries were all unreachable this way.
var (
	mappingMu sync.RWMutex

	// DriverMapping holds explicitly registered models, for the drivers whose
	// served set is narrower than the catalog or is discovered at runtime.
	DriverMapping = map[Family][]ModelID{}

	// catalogDrivers maps a driver family to the provider name it reads in
	// models.yaml. Usually those are the same string; they differ when two
	// families serve one provider's models over different transports (the
	// claude-code family serves the anthropic mappings using a subscription
	// token). Membership is explicit rather than a fallback for unknown
	// drivers, so a misspelled driver name resolves to nothing and is noticed
	// instead of silently matching the whole catalog.
	catalogDrivers = map[Family]string{}
)

// RegisterCatalogDriver declares that a driver serves exactly the models whose
// models.yaml providers list names it. Call this from the driver's init().
//
// The catalog is consulted on every lookup rather than snapshotted here,
// because the global registry can be replaced after init() — user-configured
// models arrive that way (SetGlobalRegistry), and a snapshot would leave them
// unservable for the same reason the catalog drift did.
func RegisterCatalogDriver(driver Family) {
	RegisterCatalogDriverAs(driver, driver)
}

// RegisterCatalogDriverAs declares that a driver serves the models mapped to a
// DIFFERENT provider name in models.yaml.
//
// This exists for one real case: a second transport onto the same provider's
// models. The claude-code family reaches the Anthropic model set with an
// sk-ant-oat subscription token, so it reads the `anthropic` mappings rather
// than requiring every model to carry a duplicate `claude-code` entry that
// could fall out of step with the one beside it.
func RegisterCatalogDriverAs(driver Family, catalogProvider Family) {
	mappingMu.Lock()
	defer mappingMu.Unlock()
	catalogDrivers[driver] = string(catalogProvider)
}

// RegisterDriverModels registers an explicit model list for a driver whose
// served set is not simply what the catalog maps to it.
//
// Prefer RegisterCatalogDriver. Use this only when the driver knows something
// models.yaml cannot: a curated commercial subset, a per-account entitlement,
// or models discovered at runtime.
func RegisterDriverModels(driver Family, models []ModelID) {
	mappingMu.Lock()
	defer mappingMu.Unlock()
	DriverMapping[driver] = append(DriverMapping[driver], models...)
}

// GetModelsForDriver returns all models a driver can serve.
func GetModelsForDriver(driver Family) []ModelID {
	mappingMu.RLock()
	provider, isCatalog := catalogDrivers[driver]
	explicit := DriverMapping[driver]
	mappingMu.RUnlock()

	if !isCatalog {
		return explicit
	}

	registry, err := GetRegistry()
	if err != nil {
		return explicit
	}
	definitions := registry.ListModelsByProvider(provider)
	out := make([]ModelID, 0, len(definitions))
	for _, definition := range definitions {
		out = append(out, ModelID(definition.ID))
	}
	return out
}

// CanDriverUseModel reports whether a driver can serve a specific model.
func CanDriverUseModel(driver Family, modelID ModelID) bool {
	mappingMu.RLock()
	provider, isCatalog := catalogDrivers[driver]
	explicit := DriverMapping[driver]
	mappingMu.RUnlock()

	if isCatalog {
		return catalogMapsModel(provider, modelID)
	}
	for _, id := range explicit {
		if id == modelID {
			return true
		}
	}
	return false
}

// GetDriversForModel returns every driver that can serve a model.
func GetDriversForModel(modelID ModelID) []Family {
	mappingMu.RLock()
	catalogBacked := make(map[Family]string, len(catalogDrivers))
	for driver, provider := range catalogDrivers {
		catalogBacked[driver] = provider
	}
	explicit := make(map[Family][]ModelID, len(DriverMapping))
	for driver, ids := range DriverMapping {
		explicit[driver] = ids
	}
	mappingMu.RUnlock()

	seen := make(map[Family]bool)
	var drivers []Family

	for driver, provider := range catalogBacked {
		if catalogMapsModel(provider, modelID) && !seen[driver] {
			seen[driver] = true
			drivers = append(drivers, driver)
		}
	}
	for driver, ids := range explicit {
		if seen[driver] {
			continue
		}
		for _, id := range ids {
			if id == modelID {
				seen[driver] = true
				drivers = append(drivers, driver)
				break
			}
		}
	}
	return drivers
}

// catalogMapsModel reports whether models.yaml gives modelID a provider entry
// naming this provider.
func catalogMapsModel(provider string, modelID ModelID) bool {
	registry, err := GetRegistry()
	if err != nil {
		return false
	}
	definition, ok := registry.GetDefinition(string(modelID))
	if !ok {
		return false
	}
	for _, mapping := range definition.Providers {
		if mapping.Driver == provider {
			return true
		}
	}
	return false
}
