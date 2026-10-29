package config

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// A project's .reliant/workflows and .reliant/presets are synced per MACHINE:
// each daemon's snapshot replaces that machine's catalog and nothing else
// (project_machine_catalogs). Two views are read from them:
//
//   - a machine's own catalog, which is what a run on that machine executes
//     against (a trigger resolves its workflow there);
//   - the project's union, which every listing and chat run reads
//     (project_configs.project_workflows_json / project_presets_json). One
//     file per project-relative path; when two machines carry the same path,
//     the one pushed most recently wins.
//
// So a machine that lacks a checkout cannot erase what another machine still
// provides: a file leaves the union only when no machine carries it.

// MachineCatalog is one machine's synced workflows and presets.
type MachineCatalog struct {
	DaemonID  string
	PushedAt  time.Time
	Workflows []StoredWorkflow
	Presets   []StoredPreset
}

// RemovedCatalogFile is a file a machine once provided and no longer does.
// It is kept so a miss can name where the definition used to live, which the
// snapshot that dropped it cannot say.
type RemovedCatalogFile struct {
	// Kind is "workflow" or "preset".
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	RelativePath string    `json:"relative_path"`
	RemovedAt    time.Time `json:"removed_at"`
}

// Catalog file kinds.
const (
	CatalogKindWorkflow = "workflow"
	CatalogKindPreset   = "preset"
)

// maxRemovedCatalogFiles bounds a machine's removal history; the newest win.
const maxRemovedCatalogFiles = 100

// WorkflowFileKey is the identity of a synced workflow file across machines:
// its project-relative path. A snapshot from a daemon that predates
// relative_path falls back to the root directory and its slug.
func WorkflowFileKey(w StoredWorkflow) string {
	if w.RelativePath != "" {
		return w.RelativePath
	}
	return path.Join(workflowref.Dir, w.Slug)
}

// PresetFileKey is WorkflowFileKey for presets.
func PresetFileKey(p StoredPreset) string {
	if p.RelativePath != "" {
		return p.RelativePath
	}
	return path.Join(PresetsDir, p.Name)
}

// PresetsDir is where a repository keeps its project presets.
const PresetsDir = ".reliant/presets"

// UnionWorkflows is the project's workflow catalog over every machine's: one
// file per path, from the machine that pushed it most recently (ties go to
// the lower daemon id, so the union is deterministic), sorted by path.
func UnionWorkflows(catalogs []MachineCatalog) []StoredWorkflow {
	return unionFiles(catalogs, func(c MachineCatalog) []StoredWorkflow { return c.Workflows }, WorkflowFileKey)
}

// UnionPresets is UnionWorkflows for presets.
func UnionPresets(catalogs []MachineCatalog) []StoredPreset {
	return unionFiles(catalogs, func(c MachineCatalog) []StoredPreset { return c.Presets }, PresetFileKey)
}

func unionFiles[T any](catalogs []MachineCatalog, files func(MachineCatalog) []T, key func(T) string) []T {
	ordered := append([]MachineCatalog(nil), catalogs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].PushedAt.Equal(ordered[j].PushedAt) {
			return ordered[i].PushedAt.After(ordered[j].PushedAt)
		}
		return ordered[i].DaemonID < ordered[j].DaemonID
	})
	byKey := map[string]T{}
	for _, c := range ordered {
		for _, f := range files(c) {
			k := key(f)
			if _, taken := byKey[k]; !taken {
				byKey[k] = f
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

// NextRemovedCatalogFiles is a machine's removal history after it replaced
// prev with next at now: every file prev carried and next does not is added,
// a file next carries again is dropped, and the newest entries are kept.
func NextRemovedCatalogFiles(prev, next MachineCatalog, history []RemovedCatalogFile, now time.Time) []RemovedCatalogFile {
	present := map[string]bool{}
	for _, w := range next.Workflows {
		present[CatalogKindWorkflow+":"+WorkflowFileKey(w)] = true
	}
	for _, p := range next.Presets {
		present[CatalogKindPreset+":"+PresetFileKey(p)] = true
	}
	var added []RemovedCatalogFile
	for _, w := range prev.Workflows {
		if k := WorkflowFileKey(w); !present[CatalogKindWorkflow+":"+k] {
			added = append(added, RemovedCatalogFile{Kind: CatalogKindWorkflow, Name: w.Name, RelativePath: k, RemovedAt: now})
		}
	}
	for _, p := range prev.Presets {
		if k := PresetFileKey(p); !present[CatalogKindPreset+":"+k] {
			added = append(added, RemovedCatalogFile{Kind: CatalogKindPreset, Name: p.Name, RelativePath: k, RemovedAt: now})
		}
	}
	seen := map[string]bool{}
	out := make([]RemovedCatalogFile, 0, len(added)+len(history))
	for _, r := range append(added, history...) {
		k := r.Kind + ":" + r.RelativePath
		if present[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	if len(out) > maxRemovedCatalogFiles {
		out = out[:maxRemovedCatalogFiles]
	}
	return out
}

// ParseRemovedCatalogFiles deserializes project_machine_catalogs.removed_json.
func ParseRemovedCatalogFiles(jsonStr *string) ([]RemovedCatalogFile, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var items []RemovedCatalogFile
	if err := json.Unmarshal([]byte(*jsonStr), &items); err != nil {
		return nil, fmt.Errorf("failed to parse removed catalog files JSON: %w", err)
	}
	return items, nil
}

// EncodeCatalogJSON is the stored form of a catalog column: nil for none, so
// an empty catalog reads back exactly as a machine that synced nothing.
func EncodeCatalogJSON[T any](items []T) (*string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	s := string(encoded)
	return &s, nil
}
