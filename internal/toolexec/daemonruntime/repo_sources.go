// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/repo"
)

// repoSourcesTTL is how long a project's discovered repo list is cached
// before re-walking the filesystem. Snapshots rebuild on a poll interval, so
// re-running discovery on every poll would be wasteful. New repos won't show
// up until this expires (or the daemon restarts) — acceptable for now;
// mid-session repo additions are explicitly out of scope for the initial
// multi-repo runtime contract.
const repoSourcesTTL = 60 * time.Second

type repoSourcesEntry struct {
	sources []string
	expires time.Time
}

var (
	repoSourcesMu    sync.Mutex
	repoSourcesCache = map[string]repoSourcesEntry{}
)

// discoverRepoSources returns the relative paths of nested git repos under
// projectPath, suitable for catalog.DiscoverInput.RepoSources. The empty
// project root entry ("") is filtered out — discoveryRoots adds it
// unconditionally. A best-effort cached walk; failures yield an empty list
// rather than an error so config indexing keeps working in degraded mode.
func discoverRepoSources(ctx context.Context, projectPath string) []string {
	if projectPath == "" {
		return nil
	}

	repoSourcesMu.Lock()
	if entry, ok := repoSourcesCache[projectPath]; ok && time.Now().Before(entry.expires) {
		repoSourcesMu.Unlock()
		return append([]string(nil), entry.sources...)
	}
	repoSourcesMu.Unlock()

	found, err := repo.Discover(ctx, projectPath, 0)
	if err != nil {
		return nil
	}
	sources := make([]string, 0, len(found))
	for _, f := range found {
		if f.RelativePath == "" || f.RelativePath == "." {
			continue
		}
		sources = append(sources, f.RelativePath)
	}

	repoSourcesMu.Lock()
	repoSourcesCache[projectPath] = repoSourcesEntry{
		sources: append([]string(nil), sources...),
		expires: time.Now().Add(repoSourcesTTL),
	}
	repoSourcesMu.Unlock()

	return sources
}
