// Copyright (c) 2025 Reliant Labs
package config

import (
	"os"
	"strings"
)

// Environment represents the application environment
type Environment string

const (
	EnvironmentDev  Environment = "dev"
	EnvironmentTest Environment = "test"
	EnvironmentProd Environment = "prod"
)

// GetEnvironment returns the current environment from environment variables.
//
// This resolution FAILS CLOSED: dev is reachable only by naming it, and every
// value that is not recognised — an unset variable, a typo, or the name of an
// environment that no longer exists — resolves to prod. Dev is the permissive
// tier (it drives auth bypass via IsDevelopmentEnvironment), so an unrecognised
// value must never land there: that would let a stale deployment config silently
// disable authentication with no enum left to explain why.
func GetEnvironment() Environment {
	env := strings.ToLower(os.Getenv("RELIANT_ENV"))
	if env == "" {
		env = strings.ToLower(os.Getenv("NODE_ENV"))
	}

	switch env {
	case "test", "testing":
		return EnvironmentTest
	// "e2e" is the in-cluster end-to-end harness (control-plane's
	// deploy/kcl/e2e/main.k sets RELIANT_ENV=e2e on the reliant pods). It runs
	// against fake secrets and needs the dev-tier behaviours, so it is named
	// here explicitly rather than relying on a permissive fallback.
	case "dev", "development", "local", "e2e":
		return EnvironmentDev
	default:
		return EnvironmentProd
	}
}

// IsTestEnvironment returns true if running in test environment
func IsTestEnvironment() bool {
	return GetEnvironment() == EnvironmentTest
}

// IsDevelopmentEnvironment returns true if running in development environment.
// This is the auth-bypass tier: it must be true only when dev was asked for by
// name. See GetEnvironment for why the resolution fails closed.
func IsDevelopmentEnvironment() bool {
	return GetEnvironment() == EnvironmentDev
}

// IsProductionEnvironment returns true if running in production environment
func IsProductionEnvironment() bool {
	return GetEnvironment() == EnvironmentProd
}
