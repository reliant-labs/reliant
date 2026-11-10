// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// The machine sweep's proposals reach the person through the server: each
// heartbeat's disk report carries the pending needs-approval worktrees
// (DiskReport.worktree_reclaim), the server lists them as the machine's
// worktree-reclaim inbox item, and the person's approve/decline comes back
// as worktree.reclaim_decide, recorded exactly as `reliant worktree reclaim
// approve|decline` records it, bound to the fingerprint the person was shown.
// A decline keeps the worktree until its state changes. An approval is
// recorded with who made it (decided_by); a server that can also sends it as
// worktree.reclaim_approve, which is what removes the worktree
// (worktree_reclaim_approve.go). An approval only in approvals.json, a file
// any process running as the user can write, removes nothing.

// WorktreeReclaimDecideCommand records a person's decisions about proposals.
const WorktreeReclaimDecideCommand = "worktree.reclaim_decide"

func init() {
	RegisterCommand(WorktreeReclaimDecideCommand, handleWorktreeReclaimDecide)
}

// worktreeReclaimDecideRequest is the command's payload.
type worktreeReclaimDecideRequest struct {
	Decisions []worktreereclaim.DecisionRequest `json:"decisions"`
	// DecidedBy names who decided, for the record: the server sends
	// "inbox:<user id>".
	DecidedBy string `json:"decided_by,omitempty"`
}

type worktreeReclaimDecideResponse struct {
	Results []worktreereclaim.DecisionResult `json:"results"`
}

// reclaimStateDir is the sweep's state directory; tests point it elsewhere.
var reclaimStateDir = func() (string, error) { return worktreereclaim.DefaultStateDir() }

func handleWorktreeReclaimDecide(ctx context.Context, payload []byte) ([]byte, error) {
	var req worktreeReclaimDecideRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if len(req.Decisions) == 0 || len(req.Decisions) > worktreereclaim.MaxProposals {
		return nil, fmt.Errorf("between 1 and %d decisions", worktreereclaim.MaxProposals)
	}
	dir, err := reclaimStateDir()
	if err != nil {
		return nil, err
	}
	by := strings.TrimSpace(req.DecidedBy)
	if by == "" {
		by = "inbox"
	}
	results, err := worktreereclaim.RecordDecisionsFor(ctx, dir, req.Decisions, by)
	if err != nil {
		return nil, err
	}
	recorded := 0
	for _, r := range results {
		if r.Recorded {
			recorded++
		}
	}
	logging.InfoContext(ctx, "[worktreereclaim] decisions recorded", "requested", len(req.Decisions), "recorded", recorded, "by", by)
	reclaimProposalsCache.invalidate()
	return json.Marshal(worktreeReclaimDecideResponse{Results: results})
}

// reclaimProposalsCache is the heartbeat's proposals, rebuilt only when one
// of the files they are read from changes.
var reclaimProposalsCache proposalsCache

type proposalsCache struct {
	mu    sync.Mutex
	stamp string
	value *reliantv1.WorktreeReclaimProposals
}

func (c *proposalsCache) invalidate() {
	c.mu.Lock()
	c.stamp = ""
	c.mu.Unlock()
}

// worktreeReclaimProposals is what the heartbeat carries: nil when the sweep
// is off for this daemon, has written no report, or proposes nothing.
func worktreeReclaimProposals(mode worktreereclaim.SweepMode) *reliantv1.WorktreeReclaimProposals {
	if mode != worktreereclaim.SweepOn && mode != worktreereclaim.SweepDryRun {
		return nil
	}
	dir, err := reclaimStateDir()
	if err != nil {
		return nil
	}
	return reclaimProposalsCache.get(dir)
}

func (c *proposalsCache) get(dir string) *reliantv1.WorktreeReclaimProposals {
	stamp := filesStamp(dir, "report.json", "notes.json", "approvals.json")
	c.mu.Lock()
	defer c.mu.Unlock()
	if stamp == c.stamp {
		return c.value
	}
	c.stamp, c.value = stamp, proposalsProto(dir)
	return c.value
}

// filesStamp changes when any of the named files under dir does.
func filesStamp(dir string, names ...string) string {
	var b strings.Builder
	for _, n := range names {
		if fi, err := os.Stat(filepath.Join(dir, n)); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", n, fi.Size(), fi.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&b, "%s:-;", n)
		}
	}
	return b.String()
}

func proposalsProto(dir string) *reliantv1.WorktreeReclaimProposals {
	set, err := worktreereclaim.PendingProposals(dir)
	if err != nil || set.Total == 0 {
		return nil
	}
	out := &reliantv1.WorktreeReclaimProposals{Mode: string(set.Mode), PassFinishedAtMs: set.FinishedAt.UnixMilli(), Total: int32(set.Total)}
	for _, p := range set.Proposals {
		out.Proposals = append(out.Proposals, &reliantv1.WorktreeReclaimProposal{
			Key: p.Key, Fingerprint: p.Fingerprint, Path: p.Path, Repo: p.Repo, Branch: p.Branch,
			Reason: string(p.Reason), Detail: firstLineOf(p.Detail), WorkSummary: p.Work,
			UnpushedCommits: int32(p.UnpushedCount), IdleSeconds: p.IdleSeconds,
			LastActivityMs: unixMs(p.LastActivity), SizeBytes: p.Bytes, Owner: p.Owner, RowId: p.RowID,
			ProposedAtMs: unixMs(p.ProposedAt), Note: p.Note, NoteAtMs: unixMs(p.NoteAt), Decision: string(p.Decision),
		})
	}
	return out
}

func unixMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
