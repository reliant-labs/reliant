// Copyright (c) 2025 Reliant Labs
package registry

import (
	"context"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// ModelLister is an OPTIONAL capability a credentialed client may implement when
// the set (or per-account availability) of models it serves can only be known by
// consulting the user's account — e.g. GitHub Copilot, whose per-account /models
// catalog decides which models are enabled.
//
// Clients that do NOT implement this get the default static behavior for free
// (see AvailableModelsFor): the registry's model list for their family, all
// marked enabled. This keeps per-account availability a modular provider
// capability rather than a special-case, with zero boilerplate for the static
// providers (anthropic, openai, gemini, vertex, openrouter, local, codex,
// reliant).
type ModelLister interface {
	// GetAvailableModels returns the picker-oriented models this account may use,
	// with Enabled reflecting per-account policy.
	GetAvailableModels(ctx context.Context) ([]models.ModelInfo, error)
}

// AvailableModelsFor returns the models a credentialed client serves for the
// picker. If the client implements ModelLister (a dynamic provider), its
// per-account list is returned. Otherwise the static registry list for driverID
// is returned (all enabled) — the uniform default, no per-driver code needed.
func AvailableModelsFor(ctx context.Context, client Client, driverID string) ([]models.ModelInfo, error) {
	if lister, ok := client.(ModelLister); ok {
		return lister.GetAvailableModels(ctx)
	}
	reg, err := models.GetRegistry()
	if err != nil {
		return nil, err
	}
	return reg.ModelsForDriver(driverID), nil
}

// ProviderAvailability is what a credentialed provider reports about its
// models for the connected account, keyed by the provider-facing api_model.
type ProviderAvailability struct {
	Models map[string]models.ModelAvailability

	// Authoritative means a catalog model the provider does not list is NOT
	// servable (UnlistedReason says why). When false, an unlisted model is
	// treated as servable so a stale or renamed catalog never hides a model.
	Authoritative  bool
	UnlistedReason string
}

// For returns the availability of one api_model.
func (p ProviderAvailability) For(apiModel string) models.ModelAvailability {
	if a, ok := p.Models[apiModel]; ok {
		return a
	}
	if p.Authoritative {
		return models.ModelAvailability{Disabled: true, Reason: p.UnlistedReason}
	}
	return models.ModelAvailability{}
}

// AvailabilityReporter is the OPTIONAL capability behind per-account
// availability for dynamic providers (Copilot, Codex). Implementations must
// back it with a TTL cache: resolution consults it on every request, and it must
// not put a network call in the hot path. ModelLister (the picker view) is
// derived from the same report, so the picker and resolution cannot disagree.
type AvailabilityReporter interface {
	ReportAvailability(ctx context.Context) (ProviderAvailability, error)
}

// ApplyAvailability stamps a provider's report onto its static model list: the
// per-account Enabled flag, the real context window and the usable reasoning
// levels.
func ApplyAvailability(infos []models.ModelInfo, report ProviderAvailability) []models.ModelInfo {
	for i := range infos {
		a := report.For(infos[i].APIModel)
		infos[i].Enabled = !a.Disabled
		if a.ContextWindow > 0 && a.ContextWindow < infos[i].Capabilities.MaxContextWindow {
			infos[i].Capabilities.MaxContextWindow = a.ContextWindow
		}
		if len(a.ThinkingLevels) > 0 {
			infos[i].Capabilities.ThinkingLevels = a.ThinkingLevels
		}
	}
	return infos
}

// BuildAvailabilityFunc turns per-driver reports into the per-(driver, model id)
// filter the model registry resolves with. A driver without a report, and a
// model without a mapping on that driver, is servable.
func BuildAvailabilityFunc(reg *models.ModelRegistry, reports map[string]ProviderAvailability) models.AvailabilityFunc {
	if len(reports) == 0 {
		return nil
	}
	return func(driver, modelID string) models.ModelAvailability {
		report, ok := reports[driver]
		if !ok {
			return models.ModelAvailability{}
		}
		def, ok := reg.GetDefinition(modelID)
		if !ok {
			return models.ModelAvailability{}
		}
		for _, p := range def.Providers {
			if p.Driver == driver {
				return report.For(p.APIModel)
			}
		}
		return models.ModelAvailability{}
	}
}
