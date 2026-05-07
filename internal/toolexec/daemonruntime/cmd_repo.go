// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"

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
	return json.Marshal(resp)
}
