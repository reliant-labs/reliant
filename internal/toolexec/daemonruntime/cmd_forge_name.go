// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func init() {
	RegisterCommand("forge.project_name", handleForgeProjectName)
}

// --- forge.project_name ---

type forgeProjectNameRequest struct {
	// Path is the project root to look for forge.yaml in.
	Path string `json:"path"`
}

type forgeProjectNameResponse struct {
	// HasForge is true when forge.yaml exists at the requested root.
	HasForge bool `json:"has_forge"`
	// ForgeProjectName is forge.yaml's top-level `name` — the key the
	// control plane files the project's deploy environments under. Empty
	// when there is no forge.yaml or it names nothing.
	ForgeProjectName string `json:"forge_project_name,omitempty"`
}

// handleForgeProjectName reads forge.yaml's `name` for one project root.
//
// repo.discover already returns this, but it also walks the tree looking for
// nested git repos. The API server needs the name on its own at two moments
// where discovery has either already run or is not wanted — a settled clone
// and an install mark — so this is the narrow read rather than a scan whose
// cost scales with the size of the checkout.
//
// A missing or unparseable forge.yaml is reported as "not a forge project",
// not as an error: the caller's next step is to leave the project unmarked,
// and that is the same thing it does for a repo that genuinely has no manifest.
func handleForgeProjectName(_ context.Context, payload []byte) ([]byte, error) {
	var req forgeProjectNameRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if req.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	// Statted rather than inferred from the name: a forge.yaml that exists
	// but names nothing is a forge project with a defect, not a non-forge
	// repo, and reporting it as the latter would hide the defect.
	_, statErr := os.Stat(filepath.Join(req.Path, "forge.yaml"))
	return json.Marshal(forgeProjectNameResponse{
		HasForge:         statErr == nil,
		ForgeProjectName: readForgeProjectName(req.Path),
	})
}
