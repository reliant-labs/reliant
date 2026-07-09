// Copyright (c) 2025 Reliant Labs
package models

// ModelInfo is a provider-scoped, picker-oriented view of a model: everything a
// model picker needs to render a single {provider, model} row, plus whether the
// user's account may actually use it (Enabled).
//
// It is deliberately distinct from ModelDefinition (registry/config shape) and
// from the legacy Model (request-time driver shape). This is the shape returned
// by the per-account availability path (registry.ModelLister / the drivers
// aggregator) and surfaced to the frontend via RPC.
type ModelInfo struct {
	// ID is the Reliant model id (the `id:` field in models.yaml, e.g.
	// "claude-5-sonnet"). Combined with DriverID this uniquely identifies a
	// pickable {model, provider} pair.
	ID string

	// DisplayName is the human-readable model name (defaults to ID).
	DisplayName string

	// Family is the model vendor/family (claude, openai, gemini, grok, other),
	// derived from the model id. Useful for grouping in a picker.
	Family string

	// DriverID is the provider/driver serving this model (anthropic, openai,
	// copilot, ...). This is the "account" dimension of per-account availability.
	DriverID string

	// APIModel is the provider-facing model identifier (the `api_model` on the
	// provider mapping, e.g. Copilot's dotted "claude-sonnet-5"). Carried so a
	// dynamic provider can intersect against its own catalog.
	APIModel string

	// Capabilities and Cost are copied from the registry definition so the picker
	// can render context window, tool/vision support, pricing, etc. without a
	// second lookup.
	Capabilities ModelCapabilities
	Cost         ModelCost

	// Tags are the registry tags for the model (flagship, fast, cheap, ...).
	Tags []string

	// Enabled reports whether the user's account may actually use this model.
	// Static providers set this to true for every registry model they serve; a
	// dynamic provider (e.g. Copilot) sets it per the account's catalog policy.
	Enabled bool
}

// modelInfoFromDef builds a ModelInfo for a single {definition, provider} pair.
func modelInfoFromDef(def *ModelDefinition, provider ProviderMapping, enabled bool) ModelInfo {
	name := def.Name
	if name == "" {
		name = def.ID
	}
	return ModelInfo{
		ID:           def.ID,
		DisplayName:  name,
		Family:       GetModelFamily(def.ID),
		DriverID:     provider.Driver,
		APIModel:     provider.APIModel,
		Capabilities: def.Capabilities,
		Cost:         def.Cost,
		Tags:         def.Tags,
		Enabled:      enabled,
	}
}

// ModelsForDriver returns the user-visible registry models the given driver
// serves, as ModelInfo with Enabled=true. This is the DEFAULT (static) model
// list every provider gets for free: it is derived entirely from the YAML
// provider mappings, so no per-driver code is required.
//
// A dynamic provider (one that must consult the user's account, e.g. Copilot)
// starts from this list and overrides the Enabled flag per its own catalog.
func (r *ModelRegistry) ModelsForDriver(driverID string) []ModelInfo {
	var out []ModelInfo
	for i := range r.models {
		def := &r.models[i]
		// Only user-visible models belong in a picker.
		if def.Visibility != VisibilityUser && def.Visibility != "" {
			continue
		}
		// The reliant driver additionally gates on its own allowlist (mirrors
		// the catalog handler), since not every reliant-mapped model is served.
		if driverID == "reliant" && !CanDriverUseModel("reliant", ModelID(def.ID)) {
			continue
		}
		for _, p := range def.Providers {
			if p.Driver == driverID {
				out = append(out, modelInfoFromDef(def, p, true))
				break
			}
		}
	}
	return out
}
