// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// What a confirmed clean-up may clear. Deliberately short:
//
//   - Go-format caches (GOCACHE, golangci-lint) are trimmed of entries
//     untouched for GoCacheTrimAge — NOT `go clean -cache`, which under a
//     running build produces "link: cannot open file …/go-build/…-d" (forge's
//     storage-lifecycle runbook), and whose unlink burst detached a virtiofs
//     volume on 2026-10-09. Unlinks are paced and capped like forge's GC.
//   - docker's build cache, pruned of records unused for the same age. Never
//     images, containers or volumes: a volume can be someone's database.
//
// The Go module cache (re-download breaks offline/private builds and builds in
// flight), the npm cache (a concurrent `npm ci` can fail) and XDG caches (tools
// keep state there) are reported but never cleared from here.
const (
	clearBudget        = 30 * time.Minute
	trimBatch          = 200
	trimRatePerSec     = 1000
	trimMaxRemovals    = 100_000
	dockerPruneTimeout = 10 * time.Minute
)

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

// clearable reports whether a kind may be cleared on this machine, and the
// directory it lives in for a Go-format cache.
func (w *Watcher) clearable(kind string) (dir string, reason string) {
	switch kind {
	case KindGoBuildCache, KindGolangciLintCache:
		for _, t := range w.targetList() {
			if t.Kind == kind {
				if !pathExists(t.Path) {
					return "", "no such cache on this machine"
				}
				return t.Path, ""
			}
		}
		return "", "no such cache on this machine"
	case KindDockerBuildCache:
		if w.docker == nil || !pathExists(DockerRoot) {
			return "", "docker is not used on this machine"
		}
		return "", ""
	default:
		return "", "not clearable"
	}
}

// ClearCaches starts clearing the given kinds in the background and returns at
// once with what started and what it refused. Progress shows on the report
// (DiskConsumer.clearing); when it finishes the volumes are re-measured.
func (w *Watcher) ClearCaches(kinds []string) (started []string, skipped []ClearSkip) {
	type job struct{ kind, dir string }
	var jobs []job
	seen := map[string]bool{}
	w.mu.Lock()
	for _, k := range kinds {
		if seen[k] {
			continue
		}
		seen[k] = true
		dir, reason := w.clearable(k)
		switch {
		case reason != "":
			skipped = append(skipped, ClearSkip{Kind: k, Reason: reason})
		case w.clearing[k]:
			skipped = append(skipped, ClearSkip{Kind: k, Reason: "already clearing"})
		default:
			w.clearing[k] = true
			jobs = append(jobs, job{k, dir})
			started = append(started, k)
		}
	}
	w.mu.Unlock()
	if len(jobs) == 0 {
		return started, skipped
	}
	w.signal()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), clearBudget)
		defer cancel()
		for _, j := range jobs {
			var freed uint64
			var err error
			if j.kind == KindDockerBuildCache {
				pctx, pcancel := context.WithTimeout(ctx, dockerPruneTimeout)
				freed, err = w.docker.PruneBuildCache(pctx, GoCacheTrimAge)
				pcancel()
			} else {
				var removed int
				freed, removed, err = trimGoCache(ctx, j.dir, w.now().Add(-GoCacheTrimAge))
				logging.Info("[diskwatch] Trimmed Go-format cache", "kind", j.kind, "dir", j.dir, "removed", removed)
			}
			if err != nil {
				logging.Warn("[diskwatch] Cache clean-up stopped", "kind", j.kind, "freed", FormatBytes(freed), "error", err)
			} else {
				logging.Info("[diskwatch] Cache cleared", "kind", j.kind, "freed", FormatBytes(freed))
			}
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
		for _, j := range jobs {
			delete(w.clearing, j.kind)
		}
		w.mu.Unlock()
		w.signal()
	}()
	return started, skipped
}

// trimGoCache removes the entries of a Go-format cache untouched since cutoff:
// only regular files inside the two-hex-digit shard dirs, never the top-level
// README/trim.txt. Unlinks go in batches of trimBatch at no more than
// trimRatePerSec, at most trimMaxRemovals per run, and the run stops at the
// first sign the filesystem is in distress (EMFILE, ENFILE, EIO, ENOTCONN).
func trimGoCache(ctx context.Context, root string, cutoff time.Time) (freed uint64, removed int, err error) {
	shards, err := os.ReadDir(root)
	if err != nil {
		return 0, 0, err
	}
	batchStart := time.Now()
	for _, sh := range shards {
		if !sh.IsDir() || !isShard(sh.Name()) {
			continue
		}
		dir := filepath.Join(root, sh.Name())
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			if distressed(rerr) {
				return freed, removed, rerr
			}
			continue
		}
		for _, e := range entries {
			if ctx.Err() != nil {
				return freed, removed, ctx.Err()
			}
			if removed >= trimMaxRemovals {
				return freed, removed, nil
			}
			if !e.Type().IsRegular() {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			size := allocatedBytes(info)
			if rmErr := os.Remove(filepath.Join(dir, e.Name())); rmErr != nil {
				if distressed(rmErr) {
					return freed, removed, rmErr
				}
				continue
			}
			freed += size
			removed++
			if removed%trimBatch == 0 {
				minDur := time.Duration(trimBatch) * time.Second / trimRatePerSec
				if elapsed := time.Since(batchStart); elapsed < minDur {
					select {
					case <-ctx.Done():
						return freed, removed, ctx.Err()
					case <-time.After(minDur - elapsed):
					}
				}
				batchStart = time.Now()
			}
		}
	}
	return freed, removed, nil
}

func distressed(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ENOTCONN)
}
