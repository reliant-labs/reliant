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
