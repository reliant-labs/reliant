// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// ForgeApps asks for every forge app in each repo found
	// (repo.FindForgeApps): repoDiscoverFound.ForgeApps, with
	// ForgeAppsScanned set on the answer. A server older than the field never
	// sets it, and the walk is skipped.
	ForgeApps bool `json:"forge_apps,omitempty"`
}

type repoDiscoverFound struct {
	RelativePath string `json:"relative_path"`
	Name         string `json:"name"`
	RemoteURL    string `json:"remote_url,omitempty"`
	// ForgeApps are the forge apps in this repo, when the request asked
	// (repoDiscoverRequest.ForgeApps). ForgeAppsTruncated says the walk
	// stopped early: the list is then partial, never the whole answer.
	ForgeApps          []repoDiscoverForgeApp `json:"forge_apps,omitempty"`
	ForgeAppsTruncated bool                   `json:"forge_apps_truncated,omitempty"`
}

// repoDiscoverForgeApp is one forge.yaml in a repo: its `name`, and its
// directory relative to the repo root ("/"-separated, "" for the root).
type repoDiscoverForgeApp struct {
	Subpath string `json:"subpath"`
	Name    string `json:"name"`
}

type repoDiscoverResponse struct {
	Discovered []repoDiscoverFound `json:"discovered"`
	// Truncated is true when the scan stopped before covering the project
	// (repo.Discovery.Truncated): Discovered is then partial, and
	// TruncatedReason says why. A caller shows what was found and says so.
	Truncated       bool   `json:"truncated,omitempty"`
	TruncatedReason string `json:"truncated_reason,omitempty"`
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
	// ForgeAppsScanned is true when each repo in Discovered carries the
	// result of a forge-app walk (repoDiscoverRequest.ForgeApps). Absent (a
	// daemon older than the field, or a request that did not ask) means the
	// forge apps are unknown, not that there are none.
	ForgeAppsScanned bool `json:"forge_apps_scanned,omitempty"`
}

// handleRepoDiscover scans a project directory for nested git repositories.
// Returns the empty list (not an error) when no repos are found — projects
// without git are valid.
func handleRepoDiscover(ctx context.Context, payload []byte) ([]byte, error) {
	var req repoDiscoverRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}

	found, err := repo.Discover(ctx, req.Path, repo.Options{MaxDepth: req.MaxDepth, Deadline: answerBy(ctx)})
	if err != nil {
		return nil, fmt.Errorf("repo discovery failed: %w", err)
	}

	resp := repoDiscoverResponse{
		Discovered:      make([]repoDiscoverFound, len(found.Repos)),
		Truncated:       found.Truncated,
		TruncatedReason: found.TruncatedReason,
	}
	for i, f := range found.Repos {
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
	if req.ForgeApps {
		findForgeApps(ctx, req.Path, resp.Discovered)
		resp.ForgeAppsScanned = true
	}
	return json.Marshal(resp)
}

// forgeAppTree is the tree findForgeApps walks for the repo at root: nil, the
// OS filesystem there. A test replaces it to inject read failures.
var forgeAppTree = func(root string) fs.FS { return nil }

// findForgeApps fills in each discovered repo's forge apps (repo.FindForgeApps).
// Every walk answers by the same deadline as the repo scan, so a project of
// many repos still answers in time, its later repos marked truncated. A repo
// that cannot be walked is reported truncated with no apps: unknown, not none.
func findForgeApps(ctx context.Context, projectPath string, discovered []repoDiscoverFound) {
	deadline := answerBy(ctx)
	for i := range discovered {
		root := projectPath
		if rel := discovered[i].RelativePath; rel != "" && rel != "." {
			root = filepath.Join(projectPath, rel)
		}
		scan, err := repo.FindForgeApps(ctx, root, repo.ForgeAppOptions{Deadline: deadline, FS: forgeAppTree(root)})
		if err != nil {
			discovered[i].ForgeAppsTruncated = true
			continue
		}
		discovered[i].ForgeAppsTruncated = scan.Truncated
		for _, app := range scan.Apps {
			discovered[i].ForgeApps = append(discovered[i].ForgeApps, repoDiscoverForgeApp{Subpath: app.Subpath, Name: app.Name})
		}
	}
}

// answerBy is when a scan answering under ctx's deadline stops walking, so its
// partial answer still reaches the caller: a fifth of the time left, at most a
// second, before the deadline. Zero (no bound beyond the walk's own) when ctx
// has none.
func answerBy(ctx context.Context) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		return time.Time{}
	}
	margin := time.Until(deadline) / 5
	if margin > time.Second {
		margin = time.Second
	}
	return deadline.Add(-margin)
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
