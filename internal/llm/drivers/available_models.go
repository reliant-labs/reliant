// Copyright (c) 2025 Reliant Labs
package drivers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListAvailableModels returns the union of picker-oriented models across every
// provider the user has configured, tagged by provider (DriverID) and carrying
// per-account availability (Enabled).
//
// For each configured driver it constructs a credentialed, model-less client and
// asks it (via registry.AvailableModelsFor) for the models it serves. Static
// providers return their registry list (all enabled) with zero per-driver code;
// a dynamic provider (Copilot) returns its account-specific list.
//
// It fails OPEN per provider: if a provider's client cannot be constructed or its
// availability lookup errors, that provider falls back to its static registry
// list rather than being dropped, so one flaky provider never blanks the picker.
func ListAvailableModels(ctx context.Context, userID string) ([]models.ModelInfo, error) {
	available := GetAvailableDrivers(ctx, userID)
	return aggregateAvailableModels(ctx, available, clientForDriver)
}

// clientBuilder constructs a credentialed, model-less client for a driver. It is
// the injection seam that lets aggregateAvailableModels be unit-tested with fake
// providers instead of the real driver factories.
type clientBuilder func(driverID models.DriverID, cfg models.DriverConfig) (registry.Client, error)

// aggregateAvailableModels iterates the configured drivers, asks each provider's
// client for the models it serves (via registry.AvailableModelsFor), and returns
// the union. It fails OPEN per provider: if a provider's client cannot be built
// or its availability lookup errors, that provider falls back to its static
// registry list rather than being dropped.
func aggregateAvailableModels(ctx context.Context, available models.AvailableDrivers, build clientBuilder) ([]models.ModelInfo, error) {
	reg, err := models.GetRegistry()
	if err != nil {
		return nil, err
	}

	var out []models.ModelInfo
	for driverID, cfg := range available.Drivers {
		if !cfg.IsConfigured() {
			continue
		}
		driver := string(driverID)

		infos, err := availableModelsForDriver(ctx, driverID, cfg, build)
		if err != nil {
			// Fail open: fall back to this provider's static registry list so a
			// transient error (e.g. a dynamic provider's catalog fetch failing)
			// never removes the provider from the picker.
			logging.Warn("[ListAvailableModels] provider availability failed; using static list (fail-open)",
				"driver", driver, "error", err)
			out = append(out, reg.ModelsForDriver(driver)...)
			continue
		}
		out = append(out, infos...)
	}
	return out, nil
}

// ListAvailableModelsOrEmpty is a fail-open convenience wrapper around
// ListAvailableModels: on a top-level error it logs and returns nil rather than
// propagating, for callers (e.g. the model catalog) that only use the result to
// gate/annotate and must never fail wholesale because availability lookup did.
func ListAvailableModelsOrEmpty(ctx context.Context, userID string) []models.ModelInfo {
	infos, err := ListAvailableModels(ctx, userID)
	if err != nil {
		logging.Warn("[ListAvailableModels] failed; proceeding with no availability info", "error", err)
		return nil
	}
	return infos
}

// availableModelsForDriver builds the provider's client and returns the models it
// serves for the picker.
func availableModelsForDriver(ctx context.Context, driverID models.DriverID, cfg models.DriverConfig, build clientBuilder) ([]models.ModelInfo, error) {
	client, err := build(driverID, cfg)
	if err != nil {
		return nil, err
	}
	return registry.AvailableModelsFor(ctx, client, string(driverID))
}

// clientForDriver constructs a credentialed, model-less client for the driver via
// the registered factory. Model-less because listing needs the account's creds
// but not a specific bound model; the factory tolerates an empty Model (only
// SendMessages/Stream need one, which listing never calls).
func clientForDriver(driverID models.DriverID, cfg models.DriverConfig) (registry.Client, error) {
	factory, ok := registry.GetDriverFactory(models.Family(driverID))
	if !ok {
		return nil, ErrUnsupportedFamily
	}

	opts := &llm.DriverOptions{
		ApiKey:           cfg.APIKey,
		BaseURL:          cfg.BaseURL,
		ExtraHeaders:     cfg.ExtraHeaders,
		UserID:           cfg.UserID,
		AccountUUID:      cfg.AccountUUID,
		AccountEmail:     cfg.AccountEmail,
		OrganizationUUID: cfg.OrganizationUUID,
	}
	return factory(opts)
}
