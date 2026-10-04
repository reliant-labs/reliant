// Copyright (c) 2025 Reliant Labs
//
// Registry/lookup-table package: the exported vars are populated once at init
// by the packages that register into them, then read. A getter returns the
// same map or slice header, so it moves the mutation surface without
// narrowing it.
//
//forge:exclude-contract: driver registry, model selection and API-key provider wiring; a strategy registry keyed by driver id
package drivers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

var (
	// globalAPIKeyProvider is the singleton API key provider
	globalAPIKeyProvider *APIKeyProvider
	providerMu           sync.Mutex
)

// APIKeyProvider manages API keys for drivers
type APIKeyProvider struct {
	repo db.Repository
}

// InitializeAPIKeyProvider initializes or updates the global API key provider.
// This can be called multiple times (e.g., in tests) to update the repo reference.
func InitializeAPIKeyProvider(repo db.Repository) {
	providerMu.Lock()
	defer providerMu.Unlock()
	globalAPIKeyProvider = &APIKeyProvider{
		repo: repo,
	}
}

// ErrNoAPIKeysConfigured is returned when no API keys are available
var ErrNoAPIKeysConfigured = fmt.Errorf("no API keys configured: please add an API key in Settings > API Keys")

// ErrDriverLookupFailed marks a failure to READ the user's provider settings, as
// opposed to a successful read that found no usable key. The first is a store
// problem worth retrying; the second is a verdict about the user's setup.
var ErrDriverLookupFailed = errors.New("could not read provider settings")

// LookupAvailableDrivers is GetAvailableDrivers for callers that must tell a
// failed read (error wrapping ErrDriverLookupFailed) from an empty result.
func LookupAvailableDrivers(ctx context.Context, userID string) (models.AvailableDrivers, error) {
	if globalAPIKeyProvider == nil {
		return models.AvailableDrivers{}, fmt.Errorf("%w: API key provider not initialized", ErrDriverLookupFailed)
	}
	availableDrivers, err := BuildAvailableDrivers(ctx, globalAPIKeyProvider.repo, userID)
	if err != nil {
		return models.AvailableDrivers{}, fmt.Errorf("%w: %w", ErrDriverLookupFailed, err)
	}
	return availableDrivers, nil
}

// GetAvailableDrivers returns the available drivers with their API keys
// Returns an error if no API keys are configured - users must configure keys via settings
func GetAvailableDrivers(ctx context.Context, userID string) models.AvailableDrivers {
	logging.Debug("GetAvailableDrivers called", "userID", userID, "providerInitialized", globalAPIKeyProvider != nil)

	// Check if provider is initialized
	if globalAPIKeyProvider == nil {
		logging.Error("API key provider not initialized - no drivers available")
		return models.AvailableDrivers{Drivers: make(map[models.DriverID]models.DriverConfig)}
	}

	// Build from settings (no caching for now - will add later)
	availableDrivers, err := BuildAvailableDrivers(ctx, globalAPIKeyProvider.repo, userID)
	if err != nil {
		logging.Error("Failed to build available drivers from settings", "error", err, "userID", userID)
		return models.AvailableDrivers{Drivers: make(map[models.DriverID]models.DriverConfig)}
	}

	if len(availableDrivers.Drivers) == 0 {
		logging.Warn("No API keys configured for user", "userID", userID)
	}

	logging.Debug("Successfully built available drivers", "driverCount", len(availableDrivers.Drivers), "userID", userID)
	return availableDrivers
}

// InvalidateAPIKeyCache is a no-op for now (caching will be added later)
func InvalidateAPIKeyCache() {
	// No-op: caching disabled
}
