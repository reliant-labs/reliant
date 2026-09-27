// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/reliant/internal/repo"
)

func init() {
	RegisterCommand("repo.discover", handleRepoDiscover)
}

// --- repo.discover ---

type repoDiscoverRequest struct {
	Path     string `json:"path"`
	MaxDepth int    `json:"max_depth"`
}

type repoDiscoverFound struct {
	RelativePath string `json:"relative_path"`
	Name         string `json:"name"`
	RemoteURL    string `json:"remote_url,omitempty"`
}

type repoDiscoverResponse struct {
	Discovered []repoDiscoverFound `json:"discovered"`
	// HasForge is true when forge.yaml exists at the requested root path.
	// Used by ProjectService.CreateProject to set projects.is_forge without a
	// second daemon round-trip. Detection mirrors the os.Stat check in
	// [internal/skills/catalog/forge.go].
	HasForge bool `json:"has_forge"`
	// ForgeProjectName is forge.yaml's `name` — the key the control plane
	// files the project's deploy environments under, persisted as
	// projects.forge_project_name. Empty when there is no forge.yaml or it
	// names nothing; reading it never fails discovery.
	ForgeProjectName string `json:"forge_project_name,omitempty"`
}

// handleRepoDiscover scans a project directory for nested git repositories.
// Returns the empty list (not an error) when no repos are found — projects
// without git are valid.
func handleRepoDiscover(ctx context.Context, payload []byte) ([]byte, error) {
	var req repoDiscoverRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	found, err := repo.Discover(ctx, req.Path, req.MaxDepth)
	if err != nil {
		return nil, fmt.Errorf("repo discovery failed: %w", err)
	}

	resp := repoDiscoverResponse{
		Discovered: make([]repoDiscoverFound, len(found)),
	}
	for i, f := range found {
		resp.Discovered[i] = repoDiscoverFound{
			RelativePath: f.RelativePath,
			Name:         f.Name,
			RemoteURL:    f.RemoteURL,
		}
	}
	if _, err := os.Stat(filepath.Join(req.Path, "forge.yaml")); err == nil {
		resp.HasForge = true
		resp.ForgeProjectName = readForgeProjectName(req.Path)
	}
	return json.Marshal(resp)
}

// forgeYAMLMaxBytes bounds the forge.yaml read. A real manifest is a few KB;
// the cap only keeps a pathological file from costing discovery anything.
const forgeYAMLMaxBytes = 1 << 20

// readForgeProjectName returns the top-level `name` from root/forge.yaml, or ""
// when the file is missing, oversized, unparseable, or names nothing. Best
// effort by contract: a forge.yaml forge itself would reject must not fail
// project discovery, which also registers the project's repos.
func readForgeProjectName(root string) string {
	f, err := os.Open(filepath.Join(root, "forge.yaml"))
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, forgeYAMLMaxBytes+1))
	if err != nil || len(data) > forgeYAMLMaxBytes {
		return ""
	}
	var manifest struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return ""
	}
	return strings.TrimSpace(manifest.Name)
}
