// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	}
	return json.Marshal(resp)
}
