// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// TestListStrandedSpawnToolCalls_MissesTerminalBackgroundedSpawn is the
// reproduction for spec §7.1: async spawn breaks the existing stranded-spawn
// reconciler.
//
// A background=true spawn (dispatchSpawnBackground, workflow.go:2976) writes
// tool_calls.status = 6 (Backgrounded) and a result (the handle text) AT
// DISPATCH TIME — before the child has done any work. So by the time the
// child workflow reaches a terminal status, the parent's spawn call already
// has BOTH a non-pending/executing status AND a tool_call_results row.
// ListStrandedSpawnToolCalls requires status IN (1,2) (pending/executing) AND
// NOT EXISTS a result — neither holds for a backgrounded call, so it can
// never match one, no matter how long the child has been finished with
// nothing delivered to the parent's mailbox.
func TestListStrandedSpawnToolCalls_MissesTerminalBackgroundedSpawn(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-stranded-background-spawn"
	createActivityTestChat(t, repo, chatID)

	// The child finished (terminal workflow status)...
	childWorkflowID := "wf-background-child-terminal"
	insertTestWorkflowWithParent(t, repo, childWorkflowID, chatID, nil, Completed())

	// ...but the parent's spawn call was already marked Backgrounded and
	// handed its dispatch-time handle result when the spawn started, exactly
	// as dispatchSpawnBackground does. No agent_messages completion row was
	// ever enqueued — the EnqueueAgentMessage activity failed (spec §7.1's
	// stated failure mode: "the parent never learns this spawn finished").
	insertTestSpawnToolCall(t, repo, "tc-background-stranded", chatID, chatID, &childWorkflowID, core.ToolCallStatusBackgrounded)
	now := time.Now().UTC()
	if err := repo.UpsertToolCallResult(ctx, &ToolCallResult{
		ToolCallID: "tc-background-stranded",
		Content:    "Spawned \"reviewer\" as agent_id: wf-background-child-terminal (status: running)",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("UpsertToolCallResult: %v", err)
	}

	stranded, err := repo.ListStrandedSpawnToolCalls(ctx)
	if err != nil {
		t.Fatalf("ListStrandedSpawnToolCalls: %v", err)
	}

	for _, call := range stranded {
		if call.ID == "tc-background-stranded" {
			t.Fatalf(
				"ListStrandedSpawnToolCalls returned the backgrounded spawn — the gap this test " +
					"documents (spec §7.1) has apparently already been closed by this query; " +
					"if so, the mailbox-anchored sweep this task builds is unnecessary and should not be added",
			)
		}
	}
}
