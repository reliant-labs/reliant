// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Reclaim runs one pass of forge's storage reaper now — waiting for a pass
// already running rather than starting a second — re-measures, and reports the
// free space the pass measured on the volumes the reaper relieves. It is the
// disk guard's built-in reclaimer (internal/diskguard): when a volume crosses
// the guard's reclaim threshold the pass runs at once, without waiting for the
// routine's schedule or diskwatch's own pressure bit. forge owns what it may
// remove; nothing here widens its scope.
func (w *Watcher) Reclaim(ctx context.Context) (freed uint64, summary string, err error) {
	if !w.acquireReaper(ctx) {
		return 0, "", ctx.Err()
	}
	// Killed builds' work directories first: they are this daemon's to
	// reclaim (gowork.go), and on 2026-11-03 they were most of a full disk
	// while forge's reaper, which does not own them, freed nothing.
	cfg := w.cacheTrimConfig()
	var work uint64
	if r := w.goWorkDirPass(ctx, cfg, true, "disk_guard", w.newPacer(cfg.RatePerSec)); r != nil {
		work = r.Freed
	}
	res := w.reaperPass(ctx, w.reaperPolicyPath(w.reaperConfig()), "disk_guard")
	w.releaseReaper()

	// Re-measure now, so the heartbeat carries the freed space and a fresh
	// breakdown instead of the picture from before the pass.
	w.mu.Lock()
	w.scanAt = map[uint64]time.Time{}
	w.mu.Unlock()
	w.poll(context.WithoutCancel(ctx))
	w.signal()

	if work > 0 {
		summary = "killed builds' work directories " + FormatBytes(work)
	}
	if !res.Ran {
		if work > 0 {
			return work, summary, nil
		}
		return 0, "", errors.New("forge's storage reaper did not run (the daemon log says why)")
	}
	if res.Freed > 0 {
		summary = strings.TrimPrefix(summary+", build caches and docker "+FormatBytes(res.Freed), ", ")
	}
	return work + res.Freed, summary, nil
}
