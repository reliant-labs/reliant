// Copyright (c) 2025 Reliant Labs
package temporal

import (
	"fmt"
	"strings"
)

// ServerConfig encodes options for EmbeddedServer instances.
type ServerConfig struct {
	// Database path for Temporal's SQLite storage
	DatabasePath string

	// Ephemeral mode (in-memory, for tests)
	Ephemeral bool

	// Namespaces to pre-create
	Namespaces []string

	// Frontend port (0 for auto-assign)
	FrontendPort int

	// Metrics port (0 for auto-assign)
	MetricsPort int

	// UI port (0 for auto-assign, dev mode only)
	UIPort int

	// Log level
	LogLevel string

	// SQLite pragmas (e.g., journal_mode, synchronous)
	SQLitePragmas map[string]string
}

// DefaultConfig returns a ServerConfig with sensible defaults
func DefaultConfig() *ServerConfig {
	return &ServerConfig{
		DatabasePath: "temporal.db",
		Ephemeral:    false,
		Namespaces:   []string{"reliant"},
		FrontendPort: 0,      // auto-assign
		MetricsPort:  0,      // auto-assign
		UIPort:       0,      // auto-assign
		LogLevel:     "warn", // Only show warnings and errors to reduce log noise
		SQLitePragmas: map[string]string{
			"journal_mode": "WAL",
			"synchronous":  "NORMAL",
		},
	}
}

// applyDefaults fills in missing fields with default values
func applyDefaults(cfg *ServerConfig) {
	if cfg.DatabasePath == "" && !cfg.Ephemeral {
		cfg.DatabasePath = "temporal.db"
	}
	if len(cfg.Namespaces) == 0 {
		cfg.Namespaces = []string{"reliant"}
	}
	// FrontendPort: Keep as 0 for dynamic allocation (handled in buildTemporalConfig)
	if cfg.LogLevel == "" {
		cfg.LogLevel = "warn" // Only show warnings and errors to reduce log noise
	}
	if cfg.SQLitePragmas == nil {
		cfg.SQLitePragmas = map[string]string{
			"journal_mode": "WAL",
			"synchronous":  "NORMAL",
		}
	}
}

// validateConfig checks that the configuration is valid
func validateConfig(cfg *ServerConfig) error {
	// Validate pragmas
	for pragma := range cfg.SQLitePragmas {
		if !isSupportedPragma(pragma) {
			return fmt.Errorf("unsupported SQLite pragma %q. allowed pragmas: %v", pragma, getSupportedPragmas())
		}
	}

	// Validate ephemeral mode
	if cfg.Ephemeral && cfg.DatabasePath != "" {
		return fmt.Errorf("config option DatabasePath is not supported in ephemeral mode")
	}
	if !cfg.Ephemeral && cfg.DatabasePath == "" {
		return fmt.Errorf("config option DatabasePath is required when ephemeral mode is disabled")
	}

	return nil
}

var supportedPragmas = map[string]bool{
	"journal_mode": true,
	"synchronous":  true,
}

func isSupportedPragma(pragma string) bool {
	return supportedPragmas[strings.ToLower(pragma)]
}

func getSupportedPragmas() []string {
	var pragmas []string
	for pragma := range supportedPragmas {
		pragmas = append(pragmas, pragma)
	}
	return pragmas
}
