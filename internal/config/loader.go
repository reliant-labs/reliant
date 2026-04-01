// Copyright (c) 2025 Reliant Labs
package config

import (
	"os"
	"path/filepath"
)

// Config directory names for the unified config structure
const (
	// ReliantDir is the main config directory name
	ReliantDir = ".reliant"
	// ReliantLocalDir is the local (gitignored) config directory name
	ReliantLocalDir = ".reliant.local"
	// ConfigFileName is the main config file name (without extension)
	ConfigFileName = "config"
	// MCPConfigFileName is the MCP config file name
	MCPConfigFileName = "mcp.json"
	// AgentsSubdir is the subdirectory for agent configs
	AgentsSubdir = "agents"
	// WorkflowsSubdir is the subdirectory for workflow configs
	WorkflowsSubdir = "workflows"
)

// GetUserConfigDir returns the user config directory (~/.reliant/).
// This is the canonical location for user-editable config files.
// Environment variable RELIANT_USER_CONFIG_DIR can override this.
func GetUserConfigDir() string {
	if configDir := os.Getenv("RELIANT_USER_CONFIG_DIR"); configDir != "" {
		return configDir
	}
	// Default to ~/.reliant/
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".reliant"
	}
	return filepath.Join(homeDir, ".reliant")
}

// GetAppDataDir returns the platform-specific app data directory.
// This is for internal data like databases, analytics, auth tokens.
// Environment variable RELIANT_APP_DATA_DIR can override this.
func GetAppDataDir() string {
	if appDataDir := os.Getenv("RELIANT_APP_DATA_DIR"); appDataDir != "" {
		return appDataDir
	}
	// Platform-specific defaults
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "data"
	}
	// macOS: ~/Library/Application Support/reliant
	// Linux: ~/.local/share/reliant
	// Windows: handled by Electron setting RELIANT_APP_DATA_DIR
	if configDir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(configDir, "reliant")
	}
	return filepath.Join(homeDir, ".local", "share", "reliant")
}
