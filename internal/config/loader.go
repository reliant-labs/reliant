// Copyright (c) 2025 Reliant Labs
package config

import (
	"os"
	"path/filepath"
	"sync"
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

// The directory overrides this process was configured with (CLIConfig's
// machine block, RELIANT_USER_CONFIG_DIR / RELIANT_APP_DATA_DIR): "" is the
// default location. The CLI entrypoint sets them once, through
// ConfigureDirs, before any command touches ~/.reliant; a process that loads
// no CLIConfig (the server roles) keeps the defaults.
var (
	dirsMu           sync.RWMutex
	userConfigDirSet string
	appDataDirSet    string
)

// ConfigureDirs records the process's configured user config and app data
// directories. An empty value keeps that directory's default.
func ConfigureDirs(userConfigDir, appDataDir string) {
	dirsMu.Lock()
	defer dirsMu.Unlock()
	userConfigDirSet, appDataDirSet = userConfigDir, appDataDir
}

// UseUserConfigDirForTest points GetUserConfigDir at dir for one test and
// restores the previous setting when it ends. Tests that use it must not run
// in parallel with each other, as for t.Setenv.
func UseUserConfigDirForTest(t interface{ Cleanup(func()) }, dir string) {
	dirsMu.Lock()
	previous := userConfigDirSet
	userConfigDirSet = dir
	dirsMu.Unlock()
	t.Cleanup(func() {
		dirsMu.Lock()
		defer dirsMu.Unlock()
		userConfigDirSet = previous
	})
}

// GetUserConfigDir returns the user config directory (~/.reliant/).
// This is the canonical location for user-editable config files.
// RELIANT_USER_CONFIG_DIR (ConfigureDirs) overrides it.
func GetUserConfigDir() string {
	dirsMu.RLock()
	configDir := userConfigDirSet
	dirsMu.RUnlock()
	if configDir != "" {
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
// RELIANT_APP_DATA_DIR (ConfigureDirs) overrides it.
func GetAppDataDir() string {
	dirsMu.RLock()
	appDataDir := appDataDirSet
	dirsMu.RUnlock()
	if appDataDir != "" {
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
