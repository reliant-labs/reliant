// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// durableResumeSource is every repository read a coarse fresh restart resumes
// from.
type durableResumeSource interface {
	GetWorkflowCheckpoint(ctx context.Context, workflowID string) (*db.WorkflowCheckpoint, error)
	liveBackgroundSpawnLister
	toolGrantLister
}

// toolGrantLister is the read that recovers what load_tool granted each thread.
type toolGrantLister interface {
	ListToolGrants(ctx context.Context, chatID string) ([]*db.ToolGrant, error)
}

// ResumeInputFromDurableState builds the resume parameter for a new run of
// rootWorkflowID whose predecessor was interrupted (failed, terminated,
// wedged, lost) and cannot be reset-and-replayed: the coarse fresh restart.
//
// Nothing of the dead execution's memory survives that restart, so
// everything here is read from durable rows:
//   - the position checkpoint written at node-entry / loop-iteration
//     boundaries (a missing one still yields a non-nil, empty ResumeInput:
//     resume mode stays on and the engine applies its fallbacks — workflow
//     resume_node, then the single top-level loop, then graph entry);
//   - the background spawns that never reported back, which ran as
//     goroutines inside the dead execution and died with it;
//   - each thread's load_tool grants, which lived in that execution's
//     ChildWorkflowTracker (ToolGrantsFromDurableState).
//
// Best-effort per part: a read that fails degrades that part of the resume
// and is logged, but never prevents the restart that recovers the chat.
func ResumeInputFromDurableState(ctx context.Context, repo durableResumeSource, chatID, rootWorkflowID string) *ResumeInput {
	resume := &ResumeInput{}

	checkpoint, err := repo.GetWorkflowCheckpoint(ctx, rootWorkflowID)
	if err != nil {
		logging.Warn("Failed to load workflow checkpoint - resuming with engine fallbacks",
			"workflowID", rootWorkflowID, "error", err)
	}
	if checkpoint != nil {
		resume.NodeID = checkpoint.NodeID
		resume.LoopIteration = int(checkpoint.LoopIteration)
	}

	spawns, err := ResumableSpawnsFromDurableState(ctx, repo, rootWorkflowID)
	if err != nil {
		logging.Warn("Failed to derive live background spawns - resuming without them",
			"workflowID", rootWorkflowID, "error", err)
	}
	resume.Spawns = spawns

	grants, err := ToolGrantsFromDurableState(ctx, repo, chatID)
	if err != nil {
		logging.Warn("Failed to recover load_tool grants - resuming without them; the agent reloads what it needs",
			"chatID", chatID, "workflowID", rootWorkflowID, "error", err)
	}
	resume.ToolGrants = grants

	return resume
}

// ToolGrantsFromDurableState rebuilds, per thread, the load_tool grants a
// coarse fresh restart of chatID's run hands back to its threads.
//
// A grant lives in two places, written from the same activity result: the
// workflow's per-thread record (ChildWorkflowTracker.toolGrants), and the
// load_tool call's own result row, recorded in the same transaction as the
// result content the model reads. Continue-as-new carries the first; a
// restart from the checkpoint has only the second, because the execution
// that held the first is dead. Rebuilding from the result rows is what keeps
// the restarted run's menu in step with the thread history it resumes from:
// every load_tool result that told the model a tool was loaded is a grant
// here, and nothing else is.
//
// Keyed by thread, so the root and each sub-agent get back exactly their own,
// and a relaunched spawn — which keeps its thread — keeps its grants.
//
// Every grant the chat recorded is returned, including one from an earlier
// run of the chat that completed: no durable boundary marks where a fresh run
// began, and adding one would be a second record that has to agree with
// this. It cannot widen what a run may use: the resolver offers a grant only
// where the run's own declaration could load the tool, under its tier and its
// unattended rule (research/TOOL_CAPABILITIES.md §3.1), and the model's
// history already shows that load.
func ToolGrantsFromDurableState(ctx context.Context, repo toolGrantLister, chatID string) (map[string][]string, error) {
	rows, err := repo.ListToolGrants(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("list tool grants for chat %s: %w", chatID, err)
	}
	var grants map[string][]string
	for _, row := range rows {
		if row.ThreadID == "" || len(row.Tools) == 0 {
			continue
		}
		if grants == nil {
			grants = make(map[string][]string)
		}
		grants[row.ThreadID] = mergeSortedNames(grants[row.ThreadID], row.Tools)
	}
	return grants, nil
}
