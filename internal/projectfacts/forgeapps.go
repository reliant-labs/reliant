// Copyright (c) 2025 Reliant Labs
package projectfacts

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// EVERY FORGE APP IN EVERY REPO OF A PROJECT.
//
// projects.forge_project_name is one name, read from the forge.yaml at the
// project root. A repo holding several forge apps, or a project of several
// repos each with its own forge.yaml, maps to at most one of them. The daemon's
// repo.discover, asked for forge_apps, walks each repo it finds for every
// forge.yaml (repo.FindForgeApps) and reports them per repo; this keeps
// repo_forge_apps in step with what it reports (control-plane
// docs/design/unified-shell-and-projects.md §8.1, D3).
//
// The writers are the moments a daemon is already holding the checkout:
// project creation (its repo.discover asks for them), a clone or attach
// landing, a forge topology answer, and the machine connect's reconcile
// probe. Every one is best effort: a project is usable whether or not its
// apps are known, and the next of them backfills.
//
// An older daemon ignores the request field and answers without
// forge_apps_scanned. That is UNKNOWN, and nothing is written: its repos keep
// whatever a newer daemon last reported, or stay unknown (NULL
// forge_apps_scanned_at), never "no apps".

// ForgeAppsStore is the database capability recording forge apps needs.
type ForgeAppsStore interface {
	ListReposByProject(ctx context.Context, projectID string) ([]*core.Repo, error)
	// SetRepoForgeApps records a repo's forge apps. complete replaces the
	// set and stamps the scan; otherwise apps are only added or renamed.
	SetRepoForgeApps(ctx context.Context, repoID string, apps []core.RepoForgeApp, complete bool) error
}

// RepoForgeApps is what one repo.discover reported for one repo.
type RepoForgeApps struct {
	Apps []core.RepoForgeApp
	// Complete is false when the daemon's walk of the repo stopped early:
	// Apps is then what it found until then.
	Complete bool
}

// ForgeAppsScan is the forge-app half of a repo.discover answer, keyed by
// each repo's relative path (normalizeRelPath).
type ForgeAppsScan map[string]RepoForgeApps

// ParseForgeApps reads the forge apps out of a repo.discover answer. ok is
// false when the daemon did not scan for them: one older than the scan, or a
// request that did not ask. Then nothing about the repos' apps is known.
func ParseForgeApps(payload []byte) (ForgeAppsScan, bool) {
	var resp struct {
		Discovered []struct {
			RelativePath string `json:"relative_path"`
			ForgeApps    []struct {
				Subpath string `json:"subpath"`
				Name    string `json:"name"`
			} `json:"forge_apps"`
			ForgeAppsTruncated bool `json:"forge_apps_truncated"`
		} `json:"discovered"`
		ForgeAppsScanned bool `json:"forge_apps_scanned"`
	}
	if err := json.Unmarshal(payload, &resp); err != nil || !resp.ForgeAppsScanned {
		return nil, false
	}
	scan := make(ForgeAppsScan, len(resp.Discovered))
	for _, d := range resp.Discovered {
		entry := RepoForgeApps{Complete: !d.ForgeAppsTruncated}
		seen := map[string]bool{}
		for _, app := range d.ForgeApps {
			name := strings.TrimSpace(app.Name)
			subpath := normalizeRelPath(app.Subpath)
			if name == "" || seen[subpath] {
				continue
			}
			seen[subpath] = true
			entry.Apps = append(entry.Apps, core.RepoForgeApp{Subpath: subpath, Name: name})
		}
		scan[normalizeRelPath(d.RelativePath)] = entry
	}
	return scan, true
}

// normalizeRelPath is a path relative to a project or repo root in one
// spelling whatever the daemon's OS: "/"-separated, no leading "./" or
// trailing "/", and "" for the root itself.
func normalizeRelPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	p = strings.TrimRight(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// RecordForgeApps writes scan onto the project's registered repos, matched by
// relative path. A repo the scan does not mention is left as it is (the
// probe may have looked less deep than the repo sits), and a repo the scan
// found but the project has not registered is not created here. It returns
// how many repos it wrote; a failed write is logged and skipped.
func RecordForgeApps(ctx context.Context, store ForgeAppsStore, projectID string, scan ForgeAppsScan) int {
	if store == nil || projectID == "" || len(scan) == 0 {
		return 0
	}
	repos, err := store.ListReposByProject(ctx, projectID)
	if err != nil {
		logging.WarnContext(ctx, "could not list a project's repos to record their forge apps",
			"error", err, "project_id", projectID)
		return 0
	}
	return recordForgeApps(ctx, store, projectID, repos, scan)
}

// recordForgeApps is RecordForgeApps over the repos already read.
func recordForgeApps(ctx context.Context, store ForgeAppsStore, projectID string, repos []*core.Repo, scan ForgeAppsScan) int {
	written := 0
	for _, r := range repos {
		found, ok := scan[normalizeRelPath(r.RelativePath)]
		if !ok {
			continue
		}
		apps := make([]core.RepoForgeApp, len(found.Apps))
		for i, app := range found.Apps {
			app.RepoID = r.ID
			apps[i] = app
		}
		if err := store.SetRepoForgeApps(ctx, r.ID, apps, found.Complete); err != nil {
			logging.WarnContext(ctx, "could not record a repo's forge apps",
				"error", err, "project_id", projectID, "repo_id", r.ID)
			continue
		}
		written++
	}
	return written
}

// forgeAppsReadTimeoutMs bounds the repo.discover a refresh sends. The
// daemon answers before it with what it has (its scan stops a little before
// the deadline), so a large tree costs a partial answer, not a timeout.
const forgeAppsReadTimeoutMs = 30_000

// RefreshForgeApps asks the daemon holding a checkout for the forge apps of
// every repo in it, and records them (RecordForgeApps). staleAfter > 0 skips
// the round trip when every registered repo had a complete scan within it,
// so a caller on a hot path (a forge topology read) asks at most that often.
//
// path is the checkout's path ON THAT MACHINE. Best effort: nothing here
// fails its caller, and an older daemon's answer writes nothing.
func RefreshForgeApps(
	ctx context.Context,
	daemons ForgeNameReader,
	store ForgeAppsStore,
	userID, daemonID, projectID, path string,
	staleAfter time.Duration,
) {
	if daemons == nil || store == nil {
		return
	}
	if userID == "" || daemonID == "" || projectID == "" || strings.TrimSpace(path) == "" {
		return
	}
	// Only registered repos are written, so with none there is nothing to
	// ask for.
	repos, err := store.ListReposByProject(ctx, projectID)
	if err != nil || len(repos) == 0 {
		return
	}
	if staleAfter > 0 && forgeAppsFresh(repos, staleAfter) {
		return
	}
	payload, err := json.Marshal(map[string]any{"path": path, "forge_apps": true})
	if err != nil {
		return
	}
	respBytes, err := daemons.SendDaemonCommandToDaemon(ctx, userID, daemonID, "repo.discover", payload, forgeAppsReadTimeoutMs)
	if err != nil {
		logging.InfoContext(ctx, "could not read a project's forge apps from the daemon; a later read will",
			"error", err, "project_id", projectID, "daemon_id", daemonID)
		return
	}
	scan, ok := ParseForgeApps(respBytes)
	if !ok {
		// A daemon older than the forge-app walk: unknown, nothing written.
		return
	}
	recordForgeApps(ctx, store, projectID, repos, scan)
}

// forgeAppsFresh reports whether every one of repos had a complete forge-app
// scan within staleAfter.
func forgeAppsFresh(repos []*core.Repo, staleAfter time.Duration) bool {
	cutoff := time.Now().Add(-staleAfter)
	for _, r := range repos {
		if r.ForgeAppsScannedAt == nil || r.ForgeAppsScannedAt.Before(cutoff) {
			return false
		}
	}
	return true
}
