// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// What a confirmed clean-up does: it runs forge's storage reaper
// (`forge storage auto-gc`, see reaper.go) once, the same pass the daemon
// schedules. forge owns what may be removed and how — the Go-format caches
// trimmed to their budget and age (never `go clean -cache`, which under a
// running build produces "link: cannot open file …/go-build/…-d", and whose
// unlink burst detached a virtiofs volume on 2026-10-09), unused docker images
// and build cache, never a volume, a worktree or a repo checkout. The Go module
// cache, the npm cache and XDG caches are reported but never cleared from here;
// the schedule keeps npm and the module cache inside their size budgets
// (cachetrim.go).
const clearBudget = 30 * time.Minute

// ClearCachesCommand is the daemon command that runs ClearCaches on the
// machine; ClearCachesRequest and ClearCachesResponse are its JSON payloads.
const ClearCachesCommand = "disk.clear_caches"

// ClearCachesRequest names the consumer kinds the user confirmed.
type ClearCachesRequest struct {
	Kinds []string `json:"kinds"`
}

// ClearCachesResponse says what started and what was refused.
type ClearCachesResponse struct {
	Started []string    `json:"started"`
	Skipped []ClearSkip `json:"skipped,omitempty"`
}

// ClearSkip is a kind ClearCaches would not start, and why.
type ClearSkip struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// clearable reports whether a kind may be cleared on this machine: the Go-format
// caches that exist here, and docker's build cache when docker is in use.
func (w *Watcher) clearable(kind string) (reason string) {
	switch kind {
	case KindGoBuildCache, KindGolangciLintCache:
		for _, t := range w.targetList() {
			if t.Kind == kind && pathExists(t.Path) {
				return ""
			}
		}
		return "no such cache on this machine"
	case KindDockerBuildCache:
		if w.docker == nil || !pathExists(DockerRoot) {
			return "docker is not used on this machine"
		}
		return ""
	default:
		return "not clearable"
	}
}

// ClearCaches starts clearing the given kinds in the background and returns at
// once with what started and what it refused. Progress shows on the report
// (DiskConsumer.clearing); when it finishes the volumes are re-measured.
//
// The clean-up is one run of forge's storage reaper, whatever kinds were
// ticked: forge's pass covers the Go caches and docker's build cache together,
// to forge's budgets. It waits for a scheduled pass already running rather than
// start a second one, and runs even when the schedule is switched off.
func (w *Watcher) ClearCaches(kinds []string) (started []string, skipped []ClearSkip) {
	seen := map[string]bool{}
	w.mu.Lock()
	for _, k := range kinds {
		if seen[k] {
			continue
		}
		seen[k] = true
		switch reason := w.clearable(k); {
		case reason != "":
			skipped = append(skipped, ClearSkip{Kind: k, Reason: reason})
		case w.clearing[k]:
			skipped = append(skipped, ClearSkip{Kind: k, Reason: "already clearing"})
		default:
			w.clearing[k] = true
			started = append(started, k)
		}
	}
	w.mu.Unlock()
	if len(started) == 0 {
		return started, skipped
	}
	w.signal()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), clearBudget)
		defer cancel()
		if w.acquireReaper(ctx) {
			w.reaperPass(ctx, w.reaperPolicyPath(w.reaperConfig()), "clear_caches")
			w.releaseReaper()
		} else {
			logging.Warn("[diskwatch] Cache clean-up gave up waiting for a running storage pass", "error", ctx.Err())
		}
		// Re-measure now rather than up to ConsumerScanInterval later, so the
		// freed space and the new breakdown show while the user is looking.
		// The clearing flags drop only after the re-poll has started that
		// scan, so a report never shows "done" with the old breakdown.
		w.mu.Lock()
		w.scanAt = map[uint64]time.Time{}
		w.mu.Unlock()
		w.poll(context.Background())
		w.mu.Lock()
		for _, k := range started {
			delete(w.clearing, k)
		}
		w.mu.Unlock()
		w.signal()
	}()
	return started, skipped
}
