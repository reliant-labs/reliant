// Copyright (c) 2025 Reliant Labs
package config

import (
	"os"
	"strings"
)

// Environment represents the application environment
type Environment string

const (
	EnvironmentDev     Environment = "dev"
	EnvironmentTest    Environment = "test"
	EnvironmentProd    Environment = "prod"
	EnvironmentStaging Environment = "staging"
	EnvironmentPreprod Environment = "preprod"
)

// GetEnvironment returns the current environment from environment variables
func GetEnvironment() Environment {
	env := strings.ToLower(os.Getenv("RELIANT_ENV"))
	if env == "" {
		env = strings.ToLower(os.Getenv("NODE_ENV"))
	}
	if env == "" {
		return EnvironmentProd // Default to production; dev mode must be explicitly opted into via RELIANT_ENV=dev
	}

	switch env {
	case "test", "testing":
		return EnvironmentTest
	case "staging":
		return EnvironmentStaging
	case "preprod":
		return EnvironmentPreprod
	case "production", "prod":
		return EnvironmentProd
	default:
		return EnvironmentDev
	}
}

// IsTestEnvironment returns true if running in test environment
func IsTestEnvironment() bool {
	return GetEnvironment() == EnvironmentTest
}

// IsDevelopmentEnvironment returns true if running in development environment.
// Staging and preprod are treated as dev for auth bypass and other dev-mode behaviors.
func IsDevelopmentEnvironment() bool {
	env := GetEnvironment()
	return env == EnvironmentDev || env == EnvironmentStaging || env == EnvironmentPreprod
}

// IsProductionEnvironment returns true if running in production environment
func IsProductionEnvironment() bool {
	return GetEnvironment() == EnvironmentProd
}

// IsStagingEnvironment returns true if running in staging environment
func IsStagingEnvironment() bool {
	return GetEnvironment() == EnvironmentStaging
}

// IsPreprodEnvironment returns true if running in preprod environment
func IsPreprodEnvironment() bool {
	return GetEnvironment() == EnvironmentPreprod
}

// IsRestrictedEnvironment returns true for environments that enforce domain whitelisting.
func IsRestrictedEnvironment() bool {
	env := GetEnvironment()
	return env == EnvironmentStaging || env == EnvironmentPreprod
}
