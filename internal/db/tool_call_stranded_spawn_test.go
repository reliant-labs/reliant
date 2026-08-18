// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func insertTestSpawnToolCall(t *testing.T, repo *Repo, id, chatID, threadID string, childWorkflowID *string, status core.ToolCallStatus) {
	t.Helper()
	now := time.Now().UTC()
	call := &ToolCall{
		ID:              id,
		ChatID:          chatID,
		ThreadID:        &threadID,
		ToolName:        "spawn",
		Status:          status,
		ChildWorkflowID: childWorkflowID,
		RequestedAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if status.IsTerminal() {
		call.CompletedAt = &now
	}
	if err := repo.UpsertToolCall(context.Background(), call); err != nil {
		t.Fatalf("insertTestSpawnToolCall(%s): %v", id, err)
	}
}

// TestListStrandedSpawnToolCalls pins the join from a finished sub-agent back
// to its parent.
//
// The regression: executeSpawnInline writes the child's terminal workflow
// status and the parent's tool-call result as two separate activities. A worker
// that dies between them leaves the child correctly recorded as finished while
// the parent's spawn call stays "executing" forever — the parent blocks on a
// sub-agent that is already over, and that sub-agent's work is silently
// dropped. Cleanup makes this exact repair but is scoped to the ENDING
// workflow's own thread, and a stranded call belongs to the still-live parent's
// thread, so it is outside every scope Cleanup can reach. Observed on real
// data: a child failed at 22:16:54 and its parent's spawn call was still
// executing 49 hours later.
func TestListStrandedSpawnToolCalls(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	chatID := "chat-stranded-spawn"
	createActivityTestChat(t, repo, chatID)

	// The bug, exactly: a failed child whose parent's call never closed.
	failedChild := "wf-stranded-failed"
	insertTestWorkflowWithParent(t, repo, failedChild, chatID, nil, Failed())
	insertTestSpawnToolCall(t, repo, "tc-stranded-failed", chatID, chatID, &failedChild, core.ToolCallStatusExecuting)

	// A child that COMPLETED and still never wrote its result is stranded just
	// as hard — the two writes are separate on the success path too.
	completedChild := "wf-stranded-completed"
	insertTestWorkflowWithParent(t, repo, completedChild, chatID, nil, Completed())
	insertTestSpawnToolCall(t, repo, "tc-stranded-completed", chatID, chatID, &completedChild, core.ToolCallStatusPending)

	// A LIVE child. Repairing this would fabricate a failure for a sub-agent
	// that is still working — a lie in conversation history that no later pass
	// can distinguish from a real result. This is the fail-closed direction.
	runningChild := "wf-live-child"
	insertTestWorkflowWithParent(t, repo, runningChild, chatID, nil, Active())
	insertTestSpawnToolCall(t, repo, "tc-live", chatID, chatID, &runningChild, core.ToolCallStatusExecuting)

	// Paused is resumable, not terminal: the spawn will finish and report.
	pausedChild := "wf-paused-child"
	insertTestWorkflowWithParent(t, repo, pausedChild, chatID, nil, Paused())
	insertTestSpawnToolCall(t, repo, "tc-paused", chatID, chatID, &pausedChild, core.ToolCallStatusExecuting)

	// A terminal child whose parent's call ALREADY has a result: the normal
	// path. Returning it would rewrite a real result with a synthetic one.
	settledChild := "wf-settled-child"
	insertTestWorkflowWithParent(t, repo, settledChild, chatID, nil, Completed())
	insertTestSpawnToolCall(t, repo, "tc-settled", chatID, chatID, &settledChild, core.ToolCallStatusCompleted)
	now := time.Now().UTC()
	if err := repo.UpsertToolCallResult(ctx, &ToolCallResult{
		ToolCallID: "tc-settled",
		Content:    "the sub-agent's real answer",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("UpsertToolCallResult: %v", err)
	}

	stranded, err := repo.ListStrandedSpawnToolCalls(ctx)
	if err != nil {
		t.Fatalf("ListStrandedSpawnToolCalls: %v", err)
	}

	got := map[string]bool{}
	for _, call := range stranded {
		got[call.ID] = true
	}

	for _, want := range []struct {
		id  string
		why string
	}{
		{"tc-stranded-failed", "a failed child that never reported back is the observed bug"},
		{"tc-stranded-completed", "the success path writes status and result separately too"},
	} {
		if !got[want.id] {
			t.Errorf("%s not returned — %s", want.id, want.why)
		}
	}

	for _, notWant := range []struct {
		id  string
		why string
	}{
		{"tc-live", "a running child is live work; fabricating its failure writes a lie the model reads as fact"},
		{"tc-paused", "paused is resumable — the spawn has not ended, so it has not failed to report"},
		{"tc-settled", "this call already has a real result; a repair would overwrite the sub-agent's actual answer"},
	} {
		if got[notWant.id] {
			t.Errorf("%s returned — %s", notWant.id, notWant.why)
		}
	}

	if len(stranded) != 2 {
		t.Errorf("returned %d calls, want 2 — the count is what the reconciler reports as anomalies", len(stranded))
	}
}
