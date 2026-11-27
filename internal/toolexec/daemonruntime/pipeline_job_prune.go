// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mergequeue/candidate"
)

// The merge queue's job caches (cmd_pipeline_job.go: one bare git cache per
// repository under <appdata>/queue-jobs) are this daemon's alone, so the disk
// watcher prunes them on the reaper's poll whatever the storage reaper's mode
// (diskwatch.OwnCache), and at once under disk pressure. candidate.Host.Prune
// holds them to the budgets below and never removes one a job is using.

// pipelineJobCacheName names the job caches in the disk watcher's log.
const pipelineJobCacheName = "merge_queue_job_caches"

// pipelineJobPrunePolicy is the scheduled pass: a cache unused for a week
// goes, the caches together stay within 5 GiB, and the survivors are gc'd.
var pipelineJobPrunePolicy = candidate.PrunePolicy{
	MaxAge:   candidate.DefaultPruneMaxAge,
	MaxBytes: candidate.DefaultPruneMaxBytes,
	MinIdle:  candidate.DefaultPruneMinIdle,
	GC:       true,
}

// pipelineJobUrgentPolicy is the pass while the disk is filling: a cache
// unused for a day goes, the caches together stay within 1 GiB, and nothing is
// gc'd (a repack writes its new pack before it deletes the old ones).
var pipelineJobUrgentPolicy = candidate.PrunePolicy{
	MaxAge:   24 * time.Hour,
	MaxBytes: candidate.DefaultPruneMaxBytes / 5,
	MinIdle:  10 * time.Minute,
}

// registerOwnCaches registers the caches this daemon alone creates with its
// disk watcher.
func registerOwnCaches(w *diskwatch.Watcher) {
	w.RegisterOwnCache(pipelineJobCache(pipelineJobs()))
}

// pipelineJobCache is h's job caches as a disk watcher own cache.
func pipelineJobCache(h *candidate.Host) diskwatch.OwnCache {
	return diskwatch.OwnCache{
		Name: pipelineJobCacheName,
		Path: h.Root,
		Prune: func(ctx context.Context, urgent bool) {
			policy := pipelineJobPrunePolicy
			if urgent {
				policy = pipelineJobUrgentPolicy
			}
			logPipelineJobPrune(ctx, h.Root, urgent, h.Prune(ctx, policy))
		},
	}
}

func logPipelineJobPrune(ctx context.Context, root string, urgent bool, res candidate.PruneResult) {
	for _, c := range res.Caches {
		attrs := []any{"repository", c.Repository, "bytes", c.Bytes, "last_used", c.LastUsed, "outcome", c.Outcome}
		switch {
		case c.Err != "":
			logging.WarnContext(ctx, "[pipeline-jobs] Job cache prune error", append(attrs, "error", c.Err)...)
		case c.Outcome == candidate.PruneRemovedAge || c.Outcome == candidate.PruneRemovedSize:
			logging.InfoContext(ctx, "[pipeline-jobs] Removed a job cache", attrs...)
		}
	}
	attrs := []any{"root", root, "urgent", urgent, "caches", len(res.Caches), "bytes", res.Bytes,
		"budget", res.Budget, "removed", res.Removed(), "freed", res.Freed}
	if res.Removed() > 0 {
		logging.InfoContext(ctx, "[pipeline-jobs] Job caches pruned", attrs...)
		return
	}
	logging.DebugContext(ctx, "[pipeline-jobs] Job caches within budget", attrs...)
}
