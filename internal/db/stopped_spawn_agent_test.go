// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// GetStoppedSpawnAgent is the durable answer to "did this sub-agent stop on a
// failure, and how is it resumed?" — what lets a message resume it from any
// execution of its chat, not only the one that saw it stop (prod 9fbaace8:
// the agent stopped, the run that knew completed, and the agent was stranded).
// Stopped means its thread and its latest spawn run both failed; one that
// completed, was cancelled, or has been resumed since is not.
func TestGetStoppedSpawnAgent(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-stopped-spawn-agent"
	createActivityTestChat(t, repo, chatID)
	root := chatID
	insertTestWorkflowWithParent(t, repo, root, chatID, nil, Completed())

	now := time.Now().UTC()
	spawnThread := func(id, parent string, status ThreadStatus, worktreeID *string) {
		t.Helper()
		if _, err := repo.CreateThread(ctx, &Thread{
			ID: id, ChatID: chatID, ParentThreadID: &parent, Origin: ThreadOriginSpawn,
			Status: status, WorkflowID: &id, WorktreeID: worktreeID, CreatedAt: now,
		}); err != nil {
			t.Fatalf("CreateThread(%s): %v", id, err)
		}
	}
	spawnRun := func(rowID, thread, issuer, toolCallID, issuingThread string, status WorkflowStatus, createdAt time.Time, input string) {
		t.Helper()
		if err := repo.CreateWorkflow(ctx, &Workflow{
			ID: rowID, ParentID: &issuer, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: thread, Status: status, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("CreateWorkflow(%s): %v", rowID, err)
		}
		insertTestSpawnToolCall(t, repo, toolCallID, chatID, issuingThread, &rowID, core.ToolCallStatusFailed)
		if input != "" {
			setToolInput(t, repo, toolCallID, input)
		}
	}
	report := func(id, from, to, slot string, kind core.AgentMessageKind) {
		t.Helper()
		if err := repo.EnqueueAgentMessage(ctx, &AgentMessage{
			ID: id, ChatID: chatID, FromThreadID: from, ToThreadID: to,
			Kind: kind, Body: "report", ToolCallID: &slot,
			Status: core.AgentMessageStatusQueued, CreatedAt: now,
		}); err != nil {
			t.Fatalf("EnqueueAgentMessage(%s): %v", id, err)
		}
	}

	// A top-level agent that stopped on a usage limit, in its own worktree,
	// already resumed by messages twice (slots 1 and 3; 2 never reported).
	worktreeID := "wt-stopped-agent"
	if err := repo.CreateWorktree(ctx, &Worktree{
		ID: worktreeID, Name: "agent-wt", Path: "/worktrees/agent-wt", Branch: "agent", BaseBranch: "main",
		ProjectID: "test-project", Status: 1, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	spawnThread("agent-a", root, ThreadStatusFailed, &worktreeID)
	spawnRun("agent-a", "agent-a", root, "tc-a", root, Failed(), now, `{"preset":"researcher","prompt":"look","title":"Research"}`)
	report("am-a0", "agent-a", root, "tc-a", core.AgentMessageKindFailed)
	report("am-a1", "agent-a", root, "tc-a#resume-1", core.AgentMessageKindFailed)
	report("am-a3", "agent-a", root, "tc-a#resume-3", core.AgentMessageKindFailed)

	// A nested agent, issued by agent-a, that stopped too.
	spawnThread("agent-b", "agent-a", ThreadStatusFailed, nil)
	spawnRun("agent-b", "agent-b", "agent-a", "tc-b", "agent-a", Failed(), now, "")

	// Finished agents: completed, and stopped by the user.
	spawnThread("agent-done", root, ThreadStatusCompleted, nil)
	spawnRun("agent-done", "agent-done", root, "tc-done", root, Completed(), now, "")
	spawnThread("agent-cancelled", root, ThreadStatusCancelled, nil)
	spawnRun("agent-cancelled", "agent-cancelled", root, "tc-cancelled", root, Cancelled(), now, "")

	// An agent that failed once and was then resumed by spawn(agent_id=...):
	// a newer run on the same thread, still going. It is not stopped.
	spawnThread("agent-resumed", root, ThreadStatusRunning, nil)
	spawnRun("agent-resumed", "agent-resumed", root, "tc-r1", root, Failed(), now.Add(-time.Minute), "")
	spawnRun("wf-agent-resumed-2", "agent-resumed", root, "tc-r2", root, Active(), now, "")

	// An agent resumed by spawn(agent_id=...) whose NEWER run failed: the
	// newer run is the one to resume, under its own spawn call.
	spawnThread("agent-refailed", root, ThreadStatusFailed, nil)
	spawnRun("agent-refailed", "agent-refailed", root, "tc-f1", root, Completed(), now.Add(-time.Minute), "")
	spawnRun("wf-agent-refailed-2", "agent-refailed", root, "tc-f2", root, Failed(), now, `{"agent_id":"agent-refailed","prompt":"again"}`)

	agents, listErr := repo.ListStoppedSpawnAgents(ctx, chatID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	for i, agent := range agents {
		if i > 0 && agents[i-1].SpawnDepth > agent.SpawnDepth {
			t.Fatal("stopped agents must list parents before children")
		}
		if agent.ChildThreadID == "agent-done" || agent.ChildThreadID == "agent-cancelled" {
			t.Fatalf("terminal agent revived: %s", agent.ChildThreadID)
		}
	}
	if len(agents) < 2 {
		t.Fatalf("expected stopped root and nested agents, got %d", len(agents))
	}

	got, err := repo.GetStoppedSpawnAgent(ctx, chatID, "agent-a")
	if err != nil {
		t.Fatalf("GetStoppedSpawnAgent(agent-a): %v", err)
	}
	want := StoppedSpawnAgent{
		ToolCallID: "tc-a", ParentThreadID: root, ChildThreadID: "agent-a", ChildWorkflowID: "agent-a",
		IssuingWorkflowID: root, SpawnDepth: 1, WorktreePath: "/worktrees/agent-wt", Resumes: 3,
		LastFailure: FailureReportDigest("report"),
	}
	if got == nil {
		t.Fatalf("stopped top-level agent not found; want %+v", want)
	}
	// The input column is jsonb: it comes back normalized, not byte-for-byte.
	var input struct{ Preset, Title string }
	if err := json.Unmarshal([]byte(got.ToolInput), &input); err != nil || input.Preset != "researcher" || input.Title != "Research" {
		t.Errorf("the spawn's input (preset, title) must be carried, got %q", got.ToolInput)
	}
	got.ToolInput = ""
	if *got != want {
		t.Fatalf("stopped top-level agent:\n got %+v\nwant %+v", got, want)
	}

	got, err = repo.GetStoppedSpawnAgent(ctx, chatID, "agent-b")
	if err != nil || got == nil {
		t.Fatalf("GetStoppedSpawnAgent(agent-b) = %+v, %v; want the nested stopped agent", got, err)
	}
	if got.ToolCallID != "tc-b" || got.ParentThreadID != "agent-a" || got.IssuingWorkflowID != "agent-a" || got.SpawnDepth != 2 || got.Resumes != 0 {
		t.Errorf("nested stopped agent wrong: %+v", got)
	}

	got, err = repo.GetStoppedSpawnAgent(ctx, chatID, "agent-refailed")
	if err != nil || got == nil {
		t.Fatalf("GetStoppedSpawnAgent(agent-refailed) = %+v, %v; want its newest run", got, err)
	}
	if got.ToolCallID != "tc-f2" || got.ChildWorkflowID != "wf-agent-refailed-2" {
		t.Errorf("an agent's newest run decides how it is resumed: %+v", got)
	}

	for _, thread := range []string{"agent-done", "agent-cancelled", "agent-resumed", root, "no-such-thread"} {
		got, err := repo.GetStoppedSpawnAgent(ctx, chatID, thread)
		if err != nil {
			t.Fatalf("GetStoppedSpawnAgent(%s): %v", thread, err)
		}
		if got != nil {
			t.Errorf("%s is not a sub-agent stopped on a failure, got %+v", thread, got)
		}
	}

	// Another chat cannot reach this chat's agent.
	got, err = repo.GetStoppedSpawnAgent(ctx, "another-chat", "agent-a")
	if err != nil || got != nil {
		t.Errorf("an agent is only found in its own chat: got %+v, %v", got, err)
	}
}
