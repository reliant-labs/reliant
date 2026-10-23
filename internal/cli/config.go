package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// ErrProjectConfigNotFound is returned when forge.yaml does not exist. The
// canonical sentinel lives in cmdutil (the shared leaf package) so the
// dir-nested command groups compare against the same value; this is an alias.
var ErrProjectConfigNotFound = cmdutil.ErrProjectConfigNotFound

const defaultProjectConfigFile = "forge.yaml"

// findProjectConfigFile walks upward from the resolution root looking for
// forge.yaml, similar to how git/go locate their configuration. The resolution
// root is the current working directory unless --project-dir / -C overrode it
// (cmdutil.ResolutionRoot). It returns the absolute path to the config file or
// ErrProjectConfigNotFound if no config is found before reaching the
// filesystem root.
func findProjectConfigFile() (string, error) {
	cwd, err := cmdutil.ResolutionRoot()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}

	dir := cwd
	for {
		candidate := filepath.Join(dir, defaultProjectConfigFile)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat %s: %w", candidate, err)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrProjectConfigNotFound
		}
		dir = parent
	}
}

// loadProjectConfig reads forge.yaml, walking up from the
// current working directory until it finds one (or hits the filesystem
// root). Returns ErrProjectConfigNotFound when no config is found.
func loadProjectConfig() (*config.ProjectConfig, error) {
	path, err := findProjectConfigFile()
	if err != nil {
		return nil, err
	}
	return loadProjectConfigFrom(path)
}

// loadProjectStore reads forge.yaml (walking up from cwd) and returns the
// concrete *projectstore.Store — the single read+mutate surface consumers
// route through. It is the store-returning sibling of loadProjectConfig;
// new code should prefer it so nothing outside the store impl holds a
// *config.ProjectConfig. Consumers that take the store as a dependency
// depend on their own narrow interface, not on *Store's full method set.
func loadProjectStore() (*projectstore.Store, error) {
	cfg, err := loadProjectConfig()
	if err != nil {
		return nil, err
	}
	return projectstore.New(cfg), nil
}

// loadProjectStoreFrom reads and wraps a project config at the given path
// in a *Store. Sibling of loadProjectConfigFrom.
func loadProjectStoreFrom(path string) (*projectstore.Store, error) {
	cfg, err := loadProjectConfigFrom(path)
	if err != nil {
		return nil, err
	}
	return projectstore.New(cfg), nil
}

// loadProjectConfigFrom reads and parses a project config from the given path.
// Returns ErrProjectConfigNotFound when the file does not exist.
func loadProjectConfigFrom(path string) (*config.ProjectConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrProjectConfigNotFound
		}
		return nil, fmt.Errorf("failed to read project config: %w", err)
	}

	parsed, err := config.LoadProject(data, path)
	if err != nil {
		return nil, err
	}
	cfg := *parsed

	// Path and type defaulting now happens at the config LOAD seam
	// (config.ResolveInventoryAtLoad), so every command sees the same
	// inventory rather than only the ones routing through this loader.
	// What remains here is the type-SPELLING check, which rejects rather
	// than fills and so is not a default.
	for i := range cfg.Frontends {
		if cfg.Frontends[i].Type == "" {
			continue
		}
		// Frontend type canonical forms are hyphenated ("react-native",
		// "vite-spa"). Lowercase and trim; downstream comparisons use
		// EqualFold against the hyphenated literal.
		cfg.Frontends[i].Type = strings.ToLower(strings.TrimSpace(cfg.Frontends[i].Type))
		// The snake_case spellings are not valid — the canonical spelling
		// is hyphenated. Reject them outright rather than silently
		// rewriting an out-of-date forge.yaml.
		switch cfg.Frontends[i].Type {
		case "react_native":
			return nil, fmt.Errorf("frontend %q: type \"react_native\" is not valid; use the hyphenated spelling \"react-native\"", cfg.Frontends[i].Name)
		case "vite_spa":
			return nil, fmt.Errorf("frontend %q: type \"vite_spa\" is not valid; use the hyphenated spelling \"vite-spa\"", cfg.Frontends[i].Name)
		}
	}

	return &cfg, nil
}
