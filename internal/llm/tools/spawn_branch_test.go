// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/require"
)

// branchedSpawnFixture is a conversation that spawned sub-agents and was then
// BRANCHED — the sequence that made an orchestrator lose every child it had.
//
// Observed 2026-10-06: the user branched a chat whose orchestrator was waiting
// on background spawns. The branch's root thread is a new thread
// (origin=fork, parent_thread_id = the original root), while every spawn
// tool_calls row still names the ORIGINAL thread as its issuer. spawn_status in
// the branch therefore answered "No sub-agents spawned from this thread." and
// "agent_id … is not a sub-agent spawned from this thread" for agents whose
// spawn calls were right there in the branch's own inherited transcript.
type branchedSpawnFixture struct {
	sourceChatID     string
	sourceThreadID   string
	branchThreadID   string
	inheritedChildID string // spawned BEFORE the branch point
	laterChildID     string // spawned by the original AFTER the branch point
}

func saveFixtureMessage(t *testing.T, repo db.Repository, ctx context.Context, chatID, threadID string, role reliantv1.MessageRole, content string) string {
	t.Helper()
	if _, err := repo.GetLatestContextWindow(ctx, threadID); err != nil {
		_, cwErr := repo.CreateContextWindow(ctx, &db.ContextWindow{
			ID:       chatID + ":" + threadID + ":0",
			ThreadID: threadID,
			Sequence: 0,
		})
		require.NoError(t, cwErr)
	}
	result, err := threads.NewService(repo).SaveMessage(ctx, threads.SaveMessageOpts{
		ChatID:  chatID,
		Thread:  threadID,
		Role:    int32(role),
		Content: content,
	})
	require.NoError(t, err)
	return result.MessageID
}

// seedFixtureSpawn records a spawn issued by parentThreadID from the assistant
// message issuingMessageID, exactly as the runtime persists one: a spawn child
// thread, its workflow row, and a tool_calls row naming the parent.
func seedFixtureSpawn(t *testing.T, repo db.Repository, ctx context.Context, chatID, parentThreadID, issuingMessageID, title string) string {
	t.Helper()
	now := time.Now()
	childThreadID := uuid.New().String()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &parentThreadID,
		Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusRunning, Title: &title, CreatedAt: now,
	})
	require.NoError(t, err)
	childWorkflowID := childThreadID
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childThreadID, Status: db.Active(), CreatedAt: now,
	}))
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: "toolu_" + uuid.New().String(), ChatID: chatID, ThreadID: &parentThreadID,
		MessageID: &issuingMessageID, ToolName: "spawn", Status: core.ToolCallStatusBackgrounded,
		Input:           []byte(`{"preset":"general","title":"` + title + `"}`),
		ChildWorkflowID: &childWorkflowID,
		RequestedAt:     now, CreatedAt: now, UpdatedAt: now,
	}))
	return childThreadID
}

func seedBranchedSpawnFixture(t *testing.T, repo db.Repository, ctx context.Context) branchedSpawnFixture {
	t.Helper()
	var f branchedSpawnFixture

	// The original conversation. Its root thread id is the chat id, as for
	// every chat StartChat creates.
	f.sourceChatID = uuid.New().String()
	createTestChat(t, repo, f.sourceChatID)
	f.sourceThreadID = f.sourceChatID
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: f.sourceThreadID, ChatID: f.sourceChatID, Origin: db.ThreadOriginMain,
		Status: db.ThreadStatusRunning, CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	saveFixtureMessage(t, repo, ctx, f.sourceChatID, f.sourceThreadID, reliantv1.MessageRole_MESSAGE_ROLE_USER, "build the backend")
	spawnMsg := saveFixtureMessage(t, repo, ctx, f.sourceChatID, f.sourceThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "spawning the jobs agent")
	f.inheritedChildID = seedFixtureSpawn(t, repo, ctx, f.sourceChatID, f.sourceThreadID, spawnMsg, "Jobs backend")
	seedMessage(t, repo, ctx, f.sourceChatID, f.inheritedChildID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "jobs service is half done")
	forkPoint := saveFixtureMessage(t, repo, ctx, f.sourceChatID, f.sourceThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "waiting on the jobs agent")

	// The branch, created through the same call BranchChat makes: a new chat
	// whose root thread forks the original at forkPoint.
	branchChatID := uuid.New().String()
	createTestChat(t, repo, branchChatID)
	f.branchThreadID = branchChatID
	_, _, _, err = threads.NewService(repo).CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: branchChatID, ChatID: branchChatID, WorkflowName: "builtin://agent",
			Thread: branchChatID, Status: db.Pending(), CreatedAt: time.Now(),
		},
		ThreadID:        branchChatID,
		ChatID:          branchChatID,
		ForkFromMessage: &forkPoint,
	})
	require.NoError(t, err)

	// The original keeps running after the branch and spawns again. That
	// child is not in the branch's transcript and must not appear there.
	laterMsg := saveFixtureMessage(t, repo, ctx, f.sourceChatID, f.sourceThreadID, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "spawning the invoices agent")
	f.laterChildID = seedFixtureSpawn(t, repo, ctx, f.sourceChatID, f.sourceThreadID, laterMsg, "Invoices backend")

	return f
}

// After a branch, spawn_status in the branch must still see the sub-agents
// whose spawn calls it inherited — listed, inspectable, and clearly marked as
// belonging to the original conversation.
func TestSpawnStatus_BranchedChat_SeesInheritedChildrenReadOnly(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := seedBranchedSpawnFixture(t, repo, ctx)

	tool := NewSpawnStatusTool(repo)
	branchRC := rctx.NewToolContext(ctx, f.branchThreadID, f.branchThreadID, nil, nil)

	listing, _ := runSpawnStatus(t, tool, branchRC, SpawnStatusParams{})
	require.False(t, listing.IsError, "response: %+v", listing)
	require.NotContains(t, listing.Content, "No sub-agents spawned from this thread",
		"the branch inherited a spawn call; reporting no sub-agents is the detachment bug")
	require.Contains(t, listing.Content, f.inheritedChildID,
		"a child spawned before the branch point must be listed in the branch")
	require.Contains(t, listing.Content, "read-only",
		"an inherited child must be marked as not controllable from the branch")
	require.NotContains(t, listing.Content, f.laterChildID,
		"a child the original spawned AFTER the branch point is not in the branch's transcript and must not be listed")

	single, _ := runSpawnStatus(t, tool, branchRC, SpawnStatusParams{AgentID: f.inheritedChildID})
	require.False(t, single.IsError, "inspecting an inherited child must work; response: %+v", single)
	require.Contains(t, single.Content, "jobs service is half done", "the child's last message must be readable")
	require.Contains(t, single.Content, "read-only")

	later, _ := runSpawnStatus(t, tool, branchRC, SpawnStatusParams{AgentID: f.laterChildID})
	require.True(t, later.IsError, "a post-branch child of the original is not the branch's to inspect")

	// The original conversation is unaffected: it still owns both children.
	sourceRC := rctx.NewToolContext(ctx, f.sourceChatID, f.sourceThreadID, nil, nil)
	sourceListing, _ := runSpawnStatus(t, tool, sourceRC, SpawnStatusParams{})
	require.Contains(t, sourceListing.Content, f.inheritedChildID)
	require.Contains(t, sourceListing.Content, f.laterChildID)
	require.NotContains(t, sourceListing.Content, "read-only", "the original owns its children outright")
}

// Control stays with the original conversation, and the refusal must say so —
// naming who owns the agent and why — instead of claiming the agent is
// unrelated to a chat whose own transcript spawned it.
func TestSpawnSendAndStop_BranchedChat_RefuseInheritedChildWithOwnerNamed(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := seedBranchedSpawnFixture(t, repo, ctx)
	branchRC := rctx.NewToolContext(ctx, f.branchThreadID, f.branchThreadID, nil, nil)

	send := runSpawnSend(t, NewSpawnSendTool(repo, nil), branchRC, SpawnSendParams{AgentID: f.inheritedChildID, Message: "status?"})
	require.True(t, send.IsError)
	require.Contains(t, send.Content, "branched", "the refusal must name the branch as the reason")
	require.Contains(t, send.Content, f.sourceThreadID, "the refusal must name the owning thread")
	require.NotContains(t, send.Content, "neither a sub-agent you spawned",
		"the agent was spawned by this chat's own (inherited) transcript; calling it unrelated is wrong")

	stopper := &recordingSpawnStopper{}
	stop := runSpawnStop(t, NewSpawnStopTool(repo, stopper), branchRC, SpawnStopParams{AgentID: f.inheritedChildID})
	require.True(t, stop.IsError)
	require.Contains(t, stop.Content, "branched")
	require.Contains(t, stop.Content, f.sourceThreadID)
	require.Empty(t, stopper.stops, "a branch must not be able to stop the original conversation's agent")

	// Waiting is how an orchestrator collects a result to act on, so it is
	// control too — and it must be refused fast, not after parking a budget.
	start := time.Now()
	wait, _ := runSpawnStatus(t, NewSpawnStatusTool(repo), branchRC, SpawnStatusParams{AgentID: f.inheritedChildID, Wait: true, TimeoutSeconds: 60})
	require.True(t, wait.IsError, "a branch must not wait on the original conversation's agent; response: %+v", wait)
	require.Contains(t, wait.Content, "branched")
	require.Contains(t, wait.Content, f.sourceThreadID)
	require.Less(t, time.Since(start), 5*time.Second, "the refusal must not consume the wait budget")
}
