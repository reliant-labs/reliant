// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// WorktreeReclaimApproveCommand carries a person's Inbox approval to remove
// proposed worktrees. It is the only thing that makes this daemon remove a
// worktree with work only it holds (owner decision 2026-11-10): the server
// sends it after authenticating the person and checking they may write this
// machine, over the gateway and NATS, which only the server can reach. An
// approval written to approvals.json by anything else deletes nothing.
//
// A daemon older than this command answers "unknown command"; the server
// then records the approval only (worktree.reclaim_decide), as before.
const WorktreeReclaimApproveCommand = "worktree.reclaim_approve"

func init() {
	RegisterCommand(WorktreeReclaimApproveCommand, handleWorktreeReclaimApprove)
}

type worktreeReclaimApproveRequest struct {
	Approvals []worktreeReclaimApproveItem `json:"approvals"`
	// ApprovedBy names the person, "inbox:<user id>".
	ApprovedBy string `json:"approved_by"`
	// CommandID identifies this delivery, for the journal.
	CommandID string `json:"command_id,omitempty"`
}

type worktreeReclaimApproveItem struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
}

type worktreeReclaimApproveResponse struct {
	Results []worktreereclaim.ApprovalResult `json:"results"`
}

// reclaimSweepConfig is the machine sweep's configuration, set when the
// runtime starts; a command's pass uses the same roots and idle thresholds.
var (
	reclaimSweepConfigMu sync.Mutex
	reclaimSweepConfig   = worktreereclaim.SweepConfig{Mode: worktreereclaim.SweepOff}
)

func setReclaimSweepConfig(cfg worktreereclaim.SweepConfig) {
	reclaimSweepConfigMu.Lock()
	reclaimSweepConfig = cfg
	reclaimSweepConfigMu.Unlock()
}

func currentReclaimSweepConfig() worktreereclaim.SweepConfig {
	reclaimSweepConfigMu.Lock()
	defer reclaimSweepConfigMu.Unlock()
	return reclaimSweepConfig
}

// Seams for tests: the reclaimer, and how an accepted approval's removal is
// started (in the background in the daemon, inline in a test).
var (
	approvalReclaimer = sharedWorktreeReclaimer
	startApprovalRun  = func(run func()) { go run() }
	// approvalRuns serializes removals: one at a time, after each other.
	approvalRuns sync.Mutex
)

func handleWorktreeReclaimApprove(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeReclaimApproveRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if len(req.Approvals) == 0 || len(req.Approvals) > worktreereclaim.MaxProposals {
		return nil, fmt.Errorf("between 1 and %d approvals", worktreereclaim.MaxProposals)
	}
	by := strings.TrimSpace(req.ApprovedBy)
	if !strings.HasPrefix(by, "inbox:") || len(by) == len("inbox:") {
		return nil, fmt.Errorf("approved_by must name the person (inbox:<user id>)")
	}
	dir, err := reclaimStateDir()
	if err != nil {
		return nil, err
	}
	cfg := currentReclaimSweepConfig()
	now := time.Now()
	resp := worktreeReclaimApproveResponse{Results: make([]worktreereclaim.ApprovalResult, 0, len(req.Approvals))}
	var accepted []worktreereclaim.ApprovalCommand
	for _, a := range req.Approvals {
		cmd := worktreereclaim.ApprovalCommand{Key: a.Key, Fingerprint: a.Fingerprint, Path: a.Path, ApprovedBy: by, CommandID: req.CommandID}
		res, err := worktreereclaim.PrepareApprovalCommand(ctx, dir, cfg.Mode, cmd, now)
		if err != nil {
			return nil, err
		}
		if res.Status == worktreereclaim.ApprovalAccepted && !res.Replayed {
			accepted = append(accepted, cmd)
		}
		resp.Results = append(resp.Results, res)
	}
	reclaimProposalsCache.invalidate()
	logging.InfoContext(ctx, "[worktreereclaim] approvals received", "requested", len(req.Approvals), "accepted", len(accepted), "by", by, "command_id", req.CommandID)
	if len(accepted) > 0 {
		r, err := approvalReclaimer()
		if err != nil {
			return nil, err
		}
		opts := worktreereclaim.SweepOptions{Mode: cfg.Mode, Roots: cfg.Roots, CleanIdle: cfg.CleanIdle, ApprovalIdle: cfg.ApprovalIdle,
			StateDir: dir, Version: version.Get().Version}
		startApprovalRun(func() {
			approvalRuns.Lock()
			defer approvalRuns.Unlock()
			rctx := context.WithoutCancel(ctx)
			for _, cmd := range accepted {
				res := r.RunApprovalCommand(rctx, opts, cmd)
				logging.InfoContext(rctx, "[worktreereclaim] approval carried out", "key", cmd.Key, "path", cmd.Path, "status", res.Status,
					"reason", res.Reason, "capture_ref", res.CaptureRef, "by", by)
			}
			reclaimProposalsCache.invalidate()
		})
	}
	return json.Marshal(resp)
}
