// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListLiveBackgroundSpawns is the durable record the coarse fresh restart
// relaunches from after a root execution dies at the history cap. It must
// return every still-backgrounded spawn at EVERY depth — including the ones
// whose child rows the reconciler has already reaped to failed, since that
// reap is an echo of the root dying, not the child finishing — and must NOT
// return a spawn that has already reported back.
func TestListLiveBackgroundSpawns(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-live-bg-spawns"
	createActivityTestChat(t, repo, chatID)
	root := chatID
	insertTestWorkflowWithParent(t, repo, root, chatID, nil, Failed())

	// Top-level spawn: child row already reaped to failed by the reconciler.
	child1 := "wf-child-1"
	insertTestWorkflowWithParent(t, repo, child1, chatID, &root, Failed())
	insertTestSpawnToolCall(t, repo, "tc-1", chatID, root, &child1, core.ToolCallStatusBackgrounded)
	setToolInput(t, repo, "tc-1", `{"preset":"researcher","prompt":"look","title":"Research"}`)

	// Nested spawn issued BY child-1 (parent_id is the issuing spawn's workflow),
	// from child-1's own thread.
	if _, err := repo.CreateThread(ctx, &Thread{ID: child1, ChatID: chatID, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	child2 := "wf-child-2"
	insertTestWorkflowWithParent(t, repo, child2, chatID, &child1, Failed())
	insertTestSpawnToolCall(t, repo, "tc-2", chatID, child1, &child2, core.ToolCallStatusBackgrounded)

	// A spawn that already reported back: excluded (relaunching it would
	// report twice).
	child3 := "wf-child-3"
	insertTestWorkflowWithParent(t, repo, child3, chatID, &root, Completed())
	insertTestSpawnToolCall(t, repo, "tc-3", chatID, root, &child3, core.ToolCallStatusBackgrounded)
	tc3 := "tc-3"
	if _, err := repo.CreateThread(ctx, &Thread{ID: child3, ChatID: chatID, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := repo.EnqueueAgentMessage(ctx, &AgentMessage{
		ID: "am-3", ChatID: chatID, FromThreadID: child3, ToThreadID: root,
		Kind: core.AgentMessageKindCompletion, Body: "done", ToolCallID: &tc3,
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("EnqueueAgentMessage: %v", err)
	}

	// A spawn the stranded repair already closed (status cancelled): excluded.
	child4 := "wf-child-4"
	insertTestWorkflowWithParent(t, repo, child4, chatID, &root, Failed())
	insertTestSpawnToolCall(t, repo, "tc-4", chatID, root, &child4, core.ToolCallStatusCancelled)

	// Another chat's root: never included.
	other := "chat-other-root"
	createActivityTestChat(t, repo, other)
	insertTestWorkflowWithParent(t, repo, other, other, nil, Failed())
	child5 := "wf-child-5"
	insertTestWorkflowWithParent(t, repo, child5, other, &other, Failed())
	insertTestSpawnToolCall(t, repo, "tc-5", other, other, &child5, core.ToolCallStatusBackgrounded)

	got, err := repo.ListLiveBackgroundSpawns(ctx, root)
	if err != nil {
		t.Fatalf("ListLiveBackgroundSpawns: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 live spawns (tc-1, tc-2), got %d: %+v", len(got), got)
	}
	if got[0].ToolCallID != "tc-1" || got[0].Depth != 0 || got[0].ParentThreadID != root ||
		got[0].ChildThreadID != child1 || got[0].IssuingWorkflowID != root {
		t.Errorf("top-level spawn wrong: %+v", got[0])
	}
	if string(got[0].ToolInput) == "" {
		t.Errorf("tool input must be carried for preset/title: %+v", got[0])
	}
	if got[1].ToolCallID != "tc-2" || got[1].Depth != 1 || got[1].ParentThreadID != child1 ||
		got[1].IssuingWorkflowID != child1 {
		t.Errorf("nested spawn wrong (must come after its issuer): %+v", got[1])
	}
}

func setToolInput(t *testing.T, repo *Repo, toolCallID, input string) {
	t.Helper()
	call, err := repo.GetToolCall(context.Background(), toolCallID)
	if err != nil {
		t.Fatalf("GetToolCall: %v", err)
	}
	call.Input = []byte(input)
	if err := repo.UpsertToolCall(context.Background(), call); err != nil {
		t.Fatalf("UpsertToolCall: %v", err)
	}
}
