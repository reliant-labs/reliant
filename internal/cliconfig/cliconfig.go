// Copyright (c) 2025 Reliant Labs

// Package cliconfig persists CLI-side configuration: named contexts
// (server URL + rlnt_pat_ API token + optional follow hooks) and the currently
// selected context. It lives next to the auth file under the platform config
// root (e.g. ~/Library/Application Support/reliant/cli-config.json on macOS)
// and never stores JWTs — the legacy auth file remains the JWT home.
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package cliconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const configFileName = "cli-config.json"

// EnvContext is the environment variable that selects a context, second in
// precedence after the --context flag.
const EnvContext = "RELIANT_CONTEXT"

// HookSpec is a follow-hook declaration: run Cmd via `sh -c` whenever an
// event of type On is observed. Shared schema between the config file and
// the --hook flag.
type HookSpec struct {
	On  string `json:"on"`
	Cmd string `json:"cmd"`
}

// Context is a named server + credential pair the CLI can talk to.
type Context struct {
	Server string     `json:"server,omitempty"`
	Token  string     `json:"token,omitempty"` // rlnt_pat_ API token (never a JWT)
	Hooks  []HookSpec `json:"hooks,omitempty"`
}

// Config is the on-disk CLI configuration.
type Config struct {
	CurrentContext string              `json:"current_context,omitempty"`
	Contexts       map[string]*Context `json:"contexts,omitempty"`
}

// ContextNames returns the configured context names, sorted.
func (c *Config) ContextNames() []string {
	names := make([]string, 0, len(c.Contexts))
	for name := range c.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultPath returns the OS-native location of the CLI config file,
// mirroring the auth-file convention in internal/auth/auth_file.go.
func DefaultPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	var configDir string
	switch runtime.GOOS {
	case "darwin":
		configDir = filepath.Join(homeDir, "Library", "Application Support", "reliant")
	case "windows":
		configDir = filepath.Join(homeDir, "AppData", "Roaming", "reliant")
	default:
		configDir = filepath.Join(homeDir, ".config", "reliant")
	}

	return filepath.Join(configDir, configFileName), nil
}

// Load reads the CLI config from the default path. A missing file yields an
// empty config, not an error.
func Load() (*Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom reads the CLI config from an explicit path. A missing file yields
// an empty config, not an error.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Contexts: map[string]*Context{}}, nil
		}
		return nil, fmt.Errorf("failed to read CLI config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse CLI config %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*Context{}
	}
	return &cfg, nil
}

// Save writes the CLI config to the default path (0600 — it holds tokens).
func Save(cfg *Config) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	return SaveTo(path, cfg)
}

// SaveTo writes the CLI config to an explicit path, creating parent
// directories as needed.
func SaveTo(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode CLI config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write CLI config %s: %w", path, err)
	}
	return nil
}

// Resolved describes which context (if any) applies to a command invocation.
type Resolved struct {
	// Name is the context name; empty when no context applies (legacy mode).
	Name string
	// Context is nil exactly when Name is empty.
	Context *Context
	// Source records what selected the context: "flag", "env", or
	// "current_context" ("" in legacy mode). Useful for status output.
	Source string
}

// Resolve applies the context-selection precedence:
//
//	--context flag > RELIANT_CONTEXT env > current_context > none (legacy)
//
// A context named explicitly (flag or env) must exist — that is an error.
// A dangling current_context is also an error (the config is corrupt and
// silently ignoring it would send requests to the wrong server).
func Resolve(cfg *Config, flagContext, envContext string) (Resolved, error) {
	pick := func(name, source string) (Resolved, error) {
		ctx, ok := cfg.Contexts[name]
		if !ok {
			return Resolved{}, fmt.Errorf("context %q not found (from %s); available: %s",
				name, source, strings.Join(cfg.ContextNames(), ", "))
		}
		return Resolved{Name: name, Context: ctx, Source: source}, nil
	}

	if flagContext != "" {
		return pick(flagContext, "flag")
	}
	if envContext != "" {
		return pick(envContext, "env")
	}
	if cfg.CurrentContext != "" {
		return pick(cfg.CurrentContext, "current_context")
	}
	return Resolved{}, nil
}
