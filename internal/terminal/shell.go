// Copyright (c) 2025 Reliant Labs
package terminal

import (
	"os"
	"os/exec"
	"runtime"
)

// GetDefaultShell returns the default shell for the current OS
func GetDefaultShell() (string, []string) {
	switch runtime.GOOS {
	case "windows":
		// Try PowerShell first, fallback to cmd
		// Use full path because go-pty resolves relative to cwd, not PATH
		if path, err := exec.LookPath("powershell.exe"); err == nil {
			return path, []string{"-NoLogo"}
		}
		if path, err := exec.LookPath("cmd.exe"); err == nil {
			return path, []string{"/K"}
		}
		return "C:\\Windows\\System32\\cmd.exe", []string{"/K"}
	case "darwin", "linux":
		// macOS and Linux - prefer zsh, respect user's SHELL
		shell := os.Getenv("SHELL")
		if shell == "" {
			// Default to zsh first (modern macOS default), then bash
			for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
				if _, err := os.Stat(s); err == nil {
					shell = s
					break
				}
			}
			if shell == "" {
				shell = "/bin/sh" // Last resort
			}
		}

		// Start as login shell to ensure proper initialization
		// The -l flag ensures .zshrc and .zprofile are sourced
		return shell, []string{"-l"}
	default:
		// Other Unix-like systems
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		return shell, []string{}
	}
}

// GetShellEnv returns environment variables for the shell
func GetShellEnv(workingDir string) []string {
	env := os.Environ()

	// Blacklist Reliant-specific environment variables that shouldn't propagate to user shells
	// These are internal to the Reliant backend and Electron processes
	blacklistedPrefixes := []string{
		"RELIANT_",       // All Reliant-specific vars (RELIANT_DATA_DIR, RELIANT_LOGS_PATH, etc.)
		"API_PORT",       // Backend API port
		"TEMPORAL_",      // Temporal-specific vars
		"ELECTRON_",      // Electron-specific vars
		"npm_config_",    // npm config vars from Electron's Node.js runtime
		"npm_package_",   // npm package vars
		"npm_lifecycle_", // npm lifecycle vars
		"INIT_CWD",       // npm initial working directory
	}

	blacklistedExact := []string{
		"BACKEND_PORT",
		"FRONTEND_PORT",
		"NODE_ENV",
	}

	// Filter out blacklisted environment variables
	filteredEnv := make([]string, 0, len(env))
	for _, e := range env {
		// Skip if it matches any blacklisted prefix
		skip := false
		for _, prefix := range blacklistedPrefixes {
			if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
				skip = true
				break
			}
		}

		// Skip if it matches any exact blacklisted key
		if !skip {
			for _, exact := range blacklistedExact {
				if len(e) >= len(exact)+1 && e[:len(exact)+1] == exact+"=" {
					skip = true
					break
				}
			}
		}

		if !skip {
			filteredEnv = append(filteredEnv, e)
		}
	}
	env = filteredEnv

	// Set TERM for proper terminal emulation (Unix only)
	if runtime.GOOS != "windows" {
		// xterm-256color is the standard for modern terminals and xterm.js fully supports it
		env = append(env, "TERM=xterm-256color")

		// Configure zsh to hide the partial line indicator (% sign)
		// Setting PROMPT_EOL_MARK to empty string removes the visual indicator
		env = append(env, "PROMPT_EOL_MARK=")
	}

	// Set PWD/CD if working directory is specified
	if workingDir != "" {
		if runtime.GOOS == "windows" {
			env = append(env, "CD="+workingDir)
		} else {
			env = append(env, "PWD="+workingDir)
		}
	}

	return env
}

// GetHomeDir returns the user's home directory in a cross-platform way
func GetHomeDir() string {
	if runtime.GOOS == "windows" {
		// Windows: USERPROFILE is the standard home directory
		home := os.Getenv("USERPROFILE")
		if home == "" {
			// Fallback to HOMEDRIVE + HOMEPATH
			home = os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		}
		if home == "" {
			home = "C:\\"
		}
		return home
	}

	// Unix: HOME
	home := os.Getenv("HOME")
	if home == "" {
		home = "/"
	}
	return home
}
