// Copyright (c) 2025 Reliant Labs
package models

import (
	"fmt"
	"strings"
	"time"
)

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

	AccountID    string // provider_accounts.id; "" only for local / test drivers
	AccountLabel string // the account's display label, for notices and logs (never a secret)
}

// ProviderRef names a driver, optionally one instance of it: "claude",
// "claude:<account-id>", "claude:alice@example.com", "local:<daemonID>".
type ProviderRef struct {
	Driver   DriverID
	Instance string // account id, account email/label, or daemon id; "" = any
}

// ParseProviderRef splits s on its FIRST ':' and validates the driver id.
// Everything after the colon is the instance, kept verbatim (an instance may
// itself contain ':').
func ParseProviderRef(s string) (ProviderRef, error) {
	s = strings.TrimSpace(s)
	driver, instance, hasInstance := strings.Cut(s, ":")
	if _, ok := ProviderPriority[driver]; !ok {
		return ProviderRef{}, fmt.Errorf("unknown provider %q in provider reference %q", driver, s)
	}
	if hasInstance && instance == "" {
		return ProviderRef{}, fmt.Errorf("provider reference %q has an empty instance", s)
	}
	return ProviderRef{Driver: DriverID(driver), Instance: instance}, nil
}

// String is the reference ParseProviderRef reads back.
func (r ProviderRef) String() string {
	if r.Instance == "" {
		return string(r.Driver)
	}
	return string(r.Driver) + ":" + r.Instance
}

// AccountStatus is an account's derived state. needs_sign_in outranks paused,
// which outranks limited.
type AccountStatus string

const (
	AccountAvailable   AccountStatus = "available"
	AccountLimited     AccountStatus = "limited"
	AccountPaused      AccountStatus = "paused"
	AccountNeedsSignIn AccountStatus = "needs_sign_in"
)

// AccountConfig is one connected account of a driver.
type AccountConfig struct {
	DriverConfig
	Priority     int
	Status       AccountStatus
	LimitedUntil time.Time
	// Serves reports whether this account's own catalog includes the model
	// (Codex plus vs pro). Nil means everything the driver serves.
	Serves func(modelID string) bool
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
	// Each driver's DEFAULT account: highest priority, not paused, not needing
	// sign-in. For one-shot callers with no thread (image/video, key
	// validation, catalog, probes).
	Drivers map[DriverID]DriverConfig

	// Accounts holds every usable account per driver in priority order.
	// Paused and needs-sign-in accounts are excluded; LIMITED ones are
	// included, because being limited is the account router's business, not
	// resolution's.
	Accounts map[DriverID][]AccountConfig

	// Availability reports per-(driver, model) availability for the account that
	// owns these drivers (e.g. a Copilot model policy=disabled). Nil means every
	// model of a configured driver is servable.
	Availability AvailabilityFunc

	// Unavailable holds the providers the user has a stored credential for
	// that the provider itself rejected, keyed by driver, with the
	// user-facing reason. A driver here is never in Drivers: it serves no
	// traffic until the user reconnects it. Resolution errors read this to
	// say WHY a pinned provider cannot serve, not just that it cannot.
	Unavailable map[DriverID]string
}

// GetAvailableDriversForModel returns the list of drivers that support a model AND are properly configured.
// For most drivers, this means having an API key. For local drivers, this means having a BaseURL.
func GetAvailableDriversForModel(modelID ModelID, availableDrivers AvailableDrivers) []DriverConfig {
	// Get all drivers that support this model
	supportingDrivers := GetDriversForModel(modelID)

	var configs []DriverConfig
	for _, driverID := range supportingDrivers {
		if config, exists := availableDrivers.Drivers[DriverID(driverID)]; exists && config.IsConfigured() {
			if availableDrivers.Availability != nil && availableDrivers.Availability(string(driverID), string(modelID)).Disabled {
				continue
			}
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
	if p, ok := ProviderPriority[string(id)]; ok {
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
// Native providers (anthropic, openai, gemini, vertexai) have priority 1,
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
