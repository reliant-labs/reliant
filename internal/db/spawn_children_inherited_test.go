// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// inheritanceFixture builds conversations and spawns row by row, the way the
// runtime and BranchChat persist them, so ListInheritedSpawnChildren is tested
// against the real schema rather than a stub of it.
type inheritanceFixture struct {
	t    *testing.T
	repo *Repo
	ctx  context.Context
	now  time.Time
}

func (f *inheritanceFixture) chat(title string) string {
	f.t.Helper()
	id := uuid.New().String()
	require.NoError(f.t, f.repo.CreateChat(f.ctx, &Chat{
		ID: id, Title: title, ProjectID: "test-project", UserID: "test-user",
		CreatedAt: f.now, UpdatedAt: f.now, LastActive: f.now,
	}))
	return id
}

func (f *inheritanceFixture) thread(thread *Thread) {
	f.t.Helper()
	thread.CreatedAt = f.now
	thread.Status = ThreadStatusRunning
	_, err := f.repo.CreateThread(f.ctx, thread)
	require.NoError(f.t, err)
	_, err = f.repo.CreateContextWindow(f.ctx, &ContextWindow{
		ID: thread.ChatID + ":" + thread.ID + ":0", ThreadID: thread.ID, CreatedAt: f.now,
	})
	require.NoError(f.t, err)
}

// message appends an assistant message to threadID and returns its id. Ordinal
// is passed explicitly: it is the column inheritance compares.
func (f *inheritanceFixture) message(chatID, threadID string, ordinal int64) string {
	f.t.Helper()
	id := uuid.New().String()
	require.NoError(f.t, f.repo.CreateMessage(f.ctx, &Message{
		ID: id, ChatID: chatID, ThreadID: threadID, ContextWindowID: chatID + ":" + threadID + ":0",
		Ordinal: ordinal, Seq: ordinal, Role: 2, CreatedAt: f.now, UpdatedAt: f.now,
	}))
	return id
}

// spawn records parentThreadID spawning a child from issuingMessageID.
func (f *inheritanceFixture) spawn(chatID, parentThreadID, issuingMessageID string) string {
	f.t.Helper()
	child := uuid.New().String()
	f.thread(&Thread{ID: child, ChatID: chatID, ParentThreadID: &parentThreadID, Origin: core.ThreadOriginSpawn})
	require.NoError(f.t, f.repo.CreateWorkflow(f.ctx, &Workflow{
		ID: child, ChatID: chatID, WorkflowName: "builtin://agent", Thread: child, Status: Active(), CreatedAt: f.now,
	}))
	require.NoError(f.t, f.repo.UpsertToolCall(f.ctx, &ToolCall{
		ID: "toolu_" + uuid.New().String(), ChatID: chatID, ThreadID: &parentThreadID, MessageID: &issuingMessageID,
		ToolName: "spawn", Status: core.ToolCallStatusBackgrounded, ChildWorkflowID: &child,
		RequestedAt: f.now, CreatedAt: f.now, UpdatedAt: f.now,
	}))
	return child
}

// branch creates a chat whose root thread forks parentThreadID at forkAt —
// the shape BranchChat writes (origin=fork, crossing into a new chat).
func (f *inheritanceFixture) branch(parentThreadID, forkAt string) (chatID string) {
	f.t.Helper()
	chatID = f.chat("branch")
	f.thread(&Thread{ID: chatID, ChatID: chatID, ParentThreadID: &parentThreadID, ForkAtMessageID: &forkAt, Origin: core.ThreadOriginFork})
	return chatID
}

func inheritedIDs(children []*InheritedSpawnChild) map[string]string {
	ids := map[string]string{}
	for _, c := range children {
		if c.ChildThreadID != nil {
			ids[*c.ChildThreadID] = c.SourceThreadID
		}
	}
	return ids
}

// The 2026-10-06 incident, row for row: an orchestrator spawned six agents
// from one assistant message and a seventh later, then waited on one. The user
// branched the chat at that waiting message. The branch then ran as a new
// root thread while every spawn row still named the original — and spawn_status
// in the branch found none of them.
//
// Extended one hop: a branch OF that branch must see the same inheritance,
// cut off at each fork point, and the original keeps spawning after both
// branches without any of that leaking down.
func TestListInheritedSpawnChildren_FollowsBranchChainUpToEachForkPoint(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	f := &inheritanceFixture{t: t, repo: repo, ctx: context.Background(), now: time.Now()}

	// The original conversation; its root thread id is its chat id.
	origin := f.chat("Roofing Management App")
	f.thread(&Thread{ID: origin, ChatID: origin, Origin: core.ThreadOriginMain})
	fanOut := f.message(origin, origin, 10)
	var firstWave []string
	for i := 0; i < 6; i++ {
		firstWave = append(firstWave, f.spawn(origin, origin, fanOut))
	}
	laterSpawnMsg := f.message(origin, origin, 20)
	seventh := f.spawn(origin, origin, laterSpawnMsg)
	waitingMsg := f.message(origin, origin, 94) // the spawn_status(wait) the user branched at

	branch := f.branch(origin, waitingMsg)

	// The original keeps running after the branch and spawns again.
	afterBranchMsg := f.message(origin, origin, 120)
	afterBranch := f.spawn(origin, origin, afterBranchMsg)

	inherited, err := repo.ListInheritedSpawnChildren(f.ctx, branch)
	require.NoError(t, err)
	got := inheritedIDs(inherited)
	require.Len(t, got, 7, "all seven pre-branch spawns are in the branch's transcript")
	for _, child := range append(firstWave, seventh) {
		require.Equal(t, origin, got[child], "inherited child %s must name the original thread as its owner", child)
	}
	require.NotContains(t, got, afterBranch, "a spawn issued after the fork point is not in the branch's history")
	require.Equal(t, "Roofing Management App", inherited[0].SourceChatTitle)

	// The branch spawns its own agent, then is itself branched — between
	// the two waves of the original.
	branchOwnMsg := f.message(branch, branch, 0)
	branchOwn := f.spawn(branch, branch, branchOwnMsg)
	branchLaterMsg := f.message(branch, branch, 1)
	grandchildBranch := f.branch(branch, branchOwnMsg)
	branchAfterFork := f.spawn(branch, branch, branchLaterMsg)

	chain, err := repo.ListInheritedSpawnChildren(f.ctx, grandchildBranch)
	require.NoError(t, err)
	chainIDs := inheritedIDs(chain)
	require.Equal(t, branch, chainIDs[branchOwn], "the nearest ancestor's spawn is owned by that ancestor")
	require.NotContains(t, chainIDs, branchAfterFork, "cut off at the second fork point")
	for _, child := range append(firstWave, seventh) {
		require.Equal(t, origin, chainIDs[child], "inheritance carries through a branch of a branch")
	}
	require.NotContains(t, chainIDs, afterBranch)
	require.Equal(t, branch, chain[0].SourceThreadID, "nearest ancestor first")

	// The original is not a branch and inherits nothing.
	none, err := repo.ListInheritedSpawnChildren(f.ctx, origin)
	require.NoError(t, err)
	require.Empty(t, none)
}

// A spawned agent running in FORK thread mode is also an origin=fork thread
// whose parent is the spawner — but in the SAME chat. It is a child, not a
// branch, and must not mistake its siblings for agents of its own.
func TestListInheritedSpawnChildren_SameChatForkIsNotABranch(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	f := &inheritanceFixture{t: t, repo: repo, ctx: context.Background(), now: time.Now()}

	chat := f.chat("orchestrator")
	f.thread(&Thread{ID: chat, ChatID: chat, Origin: core.ThreadOriginMain})
	spawnMsg := f.message(chat, chat, 1)
	f.spawn(chat, chat, spawnMsg)

	forkModeChild := uuid.New().String()
	f.thread(&Thread{ID: forkModeChild, ChatID: chat, ParentThreadID: &chat, ForkAtMessageID: &spawnMsg, Origin: core.ThreadOriginFork})

	inherited, err := repo.ListInheritedSpawnChildren(f.ctx, forkModeChild)
	require.NoError(t, err)
	require.Empty(t, inherited, "a same-chat fork (a fork-mode spawn) inherits no siblings")
}
