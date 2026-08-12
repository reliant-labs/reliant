// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// maxPlanAncestorWalk bounds the parent walk. Spawn depth is capped at 1 today
// (maxSpawnDepth in call_llm.go), so a sub-agent's plan is at most one hop up —
// but thread parentage is a general tree and this must not become a way for a
// cycle in the data to hang a tool call.
const maxPlanAncestorWalk = 8

// errNoPlanInChain is the sentinel every plan tool branches on. It wraps
// sql.ErrNoRows so a walk that ends without finding a plan is indistinguishable
// from a thread that never had one: both mean "call create_plan".
var errNoPlanInChain = fmt.Errorf("no plan for this thread or any ancestor: %w", sql.ErrNoRows)

// resolvedPlan is a plan together with where it was found.
type resolvedPlan struct {
	plan *db.Plan
	// inherited reports that the plan belongs to an ANCESTOR thread rather
	// than the calling one. Writers must refuse these; readers should say so,
	// because "no tasks assigned to me" and "I am looking at my parent's
	// board" are different situations for the agent to be in.
	inherited bool
	// ownerThreadID is the thread that actually owns the plan.
	ownerThreadID string
}

// resolvePlanForRead finds the plan governing a thread, walking up through
// parent threads when the thread has none of its own.
//
// A spawned sub-agent gets a fresh thread, and plans are keyed by thread, so
// before this the orchestrator's plan became invisible at exactly the moment it
// delegated — create_plan's own description had to warn "Any spawned workflows,
// agents, or sub threads will not see the plan or any associated tasks." The
// observable result over one long run: sub-agents created 9 private plans of
// their own while the root thread that did the delegating had none, and the
// parallelism the plan encoded never reached the agents doing the work.
//
// Reads walk the tree; WRITES DO NOT (see resolvePlanForWrite). A sub-agent
// editing its parent's plan concurrently with its siblings is a shared-memory
// problem — several agents mutating one task list with no conflict resolution —
// and nothing here needs it. Seeing the board is the part that was missing.
func resolvePlanForRead(ctx context.Context, repo db.Repository, threadID string) (*resolvedPlan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("no thread context available")
	}

	// The calling thread's own plan always wins: a sub-agent that made a plan
	// is working to that one, not its parent's.
	plan, err := repo.GetPlanByThreadID(ctx, threadID)
	if err == nil {
		return &resolvedPlan{plan: plan, ownerThreadID: threadID}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	currentID := threadID
	for hop := 0; hop < maxPlanAncestorWalk; hop++ {
		thread, threadErr := repo.GetThread(ctx, currentID)
		if threadErr != nil || thread == nil {
			// The thread row is unreadable, so the chain cannot be followed.
			// Report the original "no plan here", which is actionable
			// (create_plan), rather than an infrastructure error that is not.
			return nil, errNoPlanInChain
		}
		if thread.ParentThreadID == nil || *thread.ParentThreadID == "" {
			return nil, errNoPlanInChain
		}
		parentID := *thread.ParentThreadID
		if parentID == currentID {
			// Self-parent: corrupt row, not a hierarchy. Stop rather than spin.
			return nil, errNoPlanInChain
		}

		parentPlan, planErr := repo.GetPlanByThreadID(ctx, parentID)
		if planErr == nil {
			return &resolvedPlan{plan: parentPlan, inherited: true, ownerThreadID: parentID}, nil
		}
		if !errors.Is(planErr, sql.ErrNoRows) {
			return nil, planErr
		}
		currentID = parentID
	}

	return nil, errNoPlanInChain
}

// resolvePlanForWrite finds the plan a thread may MUTATE, which is only ever
// its own.
//
// Kept separate from resolvePlanForRead so the read-through can never silently
// become a write-through: an inherited plan is returned to readers and refused
// here, with a message that says which thread owns it.
func resolvePlanForWrite(ctx context.Context, repo db.Repository, threadID string) (*db.Plan, error) {
	if threadID == "" {
		return nil, fmt.Errorf("no thread context available")
	}
	return repo.GetPlanByThreadID(ctx, threadID)
}

// inheritedPlanWriteRefusal is the message a writer returns when the only plan
// in scope belongs to an ancestor. It names the owner and the two legitimate
// moves, so a sub-agent that tried to edit its parent's board is not left
// guessing why nothing happened.
//
// Returned INSTEAD of "no plan found, use create_plan": that advice is wrong
// here and would make a sub-agent create a private plan that fragments the
// parent's board — the exact behaviour observed before read-through existed,
// where sub-agents built 9 plans of their own.
func inheritedPlanWriteRefusal(ownerThreadID string) string {
	return fmt.Sprintf(
		"This plan belongs to an ancestor thread (%s) and is read-only here — several sub-agents share it, so it has one writer.\n"+
			"Use update_task to report progress on the task you were given, or create_plan to plan work of your own.",
		ownerThreadID)
}
