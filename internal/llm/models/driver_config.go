// Copyright (c) 2025 Reliant Labs
package models

import "time"

// DriverID represents a unique identifier for a driver implementation
// Examples: "openai", "anthropic", "openrouter", "bedrock"
type DriverID string

// ModelFamily represents a grouping of related models
// Examples: "claude", "gpt", "gemini", "llama"
type ModelFamily string

// DriverConfig represents configuration for a specific driver
type DriverConfig struct {
	DriverID         DriverID
	APIKey           string
	BaseURL          string            // Optional: for custom endpoints (required for local drivers)
	ExtraHeaders     map[string]string // Optional: forwarded auth/metadata headers for custom endpoints
	Enabled          bool              // Whether this driver is available for use
	UserID           string            // Optional: Reliant (Supabase) user ID for device ID generation
	AccountUUID      string            // Optional: Claude OAuth account UUID
	AccountEmail     string            // Optional: Claude OAuth account email
	OrganizationUUID string            // Optional: Claude OAuth organization UUID
	RefreshToken     string            // Optional: Claude OAuth refresh token for auto-refresh
	TokenExpiresAt   time.Time         // Optional: when the access token expires
}

// IsConfigured returns true if the driver has the required configuration.
// Most drivers require an API key, but local drivers only require a BaseURL.
func (c DriverConfig) IsConfigured() bool {
	if !c.Enabled {
		return false
	}
	// Local drivers don't need an API key, they need a BaseURL
	if c.DriverID == "local" {
		return c.BaseURL != ""
	}
	// All other drivers require an API key
	return c.APIKey != ""
}

// AvailableDrivers represents the set of configured drivers that can be used
type AvailableDrivers struct {
	// Map of driver ID to its configuration
	Drivers map[DriverID]DriverConfig
}

// GetAvailableDriversForModel returns the list of drivers that support a model AND are properly configured.
// For most drivers, this means having an API key. For local drivers, this means having a BaseURL.
func GetAvailableDriversForModel(modelID ModelID, availableDrivers AvailableDrivers) []DriverConfig {
	// Get all drivers that support this model
	supportingDrivers := GetDriversForModel(modelID)

	var configs []DriverConfig
	for _, driverID := range supportingDrivers {
		if config, exists := availableDrivers.Drivers[DriverID(driverID)]; exists && config.IsConfigured() {
			configs = append(configs, config)
		}
	}

	return configs
}

// ManagedDriverID identifies the Reliant-managed driver ("reliant"). Requests
// through it are billed to Reliant, unlike every other driver, which uses the
// user's own (BYO) credentials or subscription.
const ManagedDriverID DriverID = "reliant"

// IsManagedDriver reports whether the driver spends Reliant-managed credit
// rather than the user's own credentials/subscription.
func IsManagedDriver(id DriverID) bool {
	return id == ManagedDriverID
}

// driverPriority returns the ProviderPriority rank for a driver. Unknown
// drivers rank last.
func driverPriority(id DriverID) int {
	if p, ok := ProviderPriority[string(id)]; ok && p != 0 {
		return p
	}
	return 99
}

// preferDriver reports whether driver a should be selected over driver b.
// The ordering is total and deterministic — it never depends on map iteration
// order:
//  1. lower ProviderPriority wins
//  2. on priority ties, user-owned (BYO) credentials beat the managed reliant
//     driver — a user who connected their own subscription expects it to be
//     used, and it doesn't spend Reliant-managed credit
//  3. remaining ties break lexicographically by driver ID
func preferDriver(a, b DriverID) bool {
	pa, pb := driverPriority(a), driverPriority(b)
	if pa != pb {
		return pa < pb
	}
	if am, bm := IsManagedDriver(a), IsManagedDriver(b); am != bm {
		return bm // the non-managed (BYO) driver wins the tie
	}
	return a < b
}

// SelectBestDriver selects the best available driver for a model.
// Priority order: Uses ProviderPriority from registry_v2.go.
// Native providers (anthropic, openai, gemini, xai, vertexai) have priority 1,
// local providers priority 2, aggregators (openrouter) priority 10.
//
// Selection is deterministic: candidates are ranked with preferDriver, so ties
// are broken explicitly (BYO over managed, then driver ID) rather than by the
// map iteration order of the candidate set.
func SelectBestDriver(modelID ModelID, availableDrivers AvailableDrivers) (DriverConfig, bool) {
	availableConfigs := GetAvailableDriversForModel(modelID, availableDrivers)
	if len(availableConfigs) == 0 {
		return DriverConfig{}, false
	}

	bestConfig := availableConfigs[0]
	for _, config := range availableConfigs[1:] {
		if preferDriver(config.DriverID, bestConfig.DriverID) {
			bestConfig = config
		}
	}

	return bestConfig, true
}
