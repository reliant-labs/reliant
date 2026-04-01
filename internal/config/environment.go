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

// IsDevelopmentEnvironment returns true if running in development environment
func IsDevelopmentEnvironment() bool {
	return GetEnvironment() == EnvironmentDev
}

// IsProductionEnvironment returns true if running in production environment
func IsProductionEnvironment() bool {
	return GetEnvironment() == EnvironmentProd
}
