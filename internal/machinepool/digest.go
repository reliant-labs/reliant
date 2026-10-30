// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/reliant-labs/reliant/internal/config"
)

// CatalogDigest names a machine's synced project catalog (its
// project_machine_catalogs workflows and presets) by content, so two members
// can be compared: equal digests run the same workflow definitions.
//
// It hashes each file's identity (slug or name, and the path relative to the
// project root) with its content hash, sorted, so neither the order a daemon
// listed its files in nor anything machine-specific (absolute paths, push
// time) moves it. A file with no recorded content hash contributes its YAML.
// "" means the machine synced no catalog for the project.
func CatalogDigest(workflowsJSON, presetsJSON *string) (string, error) {
	if workflowsJSON == nil && presetsJSON == nil {
		return "", nil
	}
	var lines []string
	if workflowsJSON != nil && *workflowsJSON != "" {
		var wfs []config.StoredWorkflow
		if err := json.Unmarshal([]byte(*workflowsJSON), &wfs); err != nil {
			return "", fmt.Errorf("read synced workflows: %w", err)
		}
		for _, w := range wfs {
			lines = append(lines, entry("workflow", w.Slug, w.RelativePath, w.ContentHash, w.YAMLContent))
		}
	}
	if presetsJSON != nil && *presetsJSON != "" {
		var ps []config.StoredPreset
		if err := json.Unmarshal([]byte(*presetsJSON), &ps); err != nil {
			return "", fmt.Errorf("read synced presets: %w", err)
		}
		for _, p := range ps {
			lines = append(lines, entry("preset", p.Name, p.RelativePath, p.ContentHash, p.YAMLContent))
		}
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func entry(kind, name, rel, contentHash, yaml string) string {
	if contentHash == "" {
		sum := sha256.Sum256([]byte(yaml))
		contentHash = "yaml:" + hex.EncodeToString(sum[:])
	}
	return kind + "\x00" + name + "\x00" + rel + "\x00" + contentHash
}
