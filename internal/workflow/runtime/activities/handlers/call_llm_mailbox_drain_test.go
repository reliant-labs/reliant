// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CallLLM OWNS MAILBOX DELIVERY (specs/thread-interrupt.md)
//
// Delivery happens inside CallLLM, immediately before the history read. These
// tests pin the two properties that motivated putting it there:
//
//   - Delivery happens as part of assembling the history that goes to the
//     provider, so a message queued while the agent was working is part of the
//     turn it is about to take.
//   - It does not depend on the graph having a `loop` node. gsd.yaml and
//     one-ring.yaml call an LLM with no loop, so the boundary drain never ran
//     for them and queued messages were stranded forever.
//
// These test the delivery seam (drainAgentMailbox) rather than a full CallLLM
// run, which would need a live LLM driver. The seam is what executeCore calls,
// on every path, before it loads history.

// queueHumanMessage puts a human message in a thread's mailbox, exactly as
// SendAgentMessage does.
func queueHumanMessage(t *testing.T, repo db.Repository, chatID, thread, body string) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, repo.EnqueueAgentMessage(context.Background(), &db.AgentMessage{
		ID:           id,
		ChatID:       chatID,
		FromThreadID: chatID,
		ToThreadID:   thread,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         body,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))
	return id
}

// threadBodies returns the message bodies on a thread, oldest first. Text lives
// in content blocks, not on the message row.
func threadBodies(t *testing.T, repo db.Repository, chatID, thread string) []string {
	t.Helper()
	msgs, err := repo.ListMessages(context.Background(), chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var out []string
	for _, m := range msgs {
		if m.ThreadID != thread {
			continue
		}
		blocks, err := repo.ListContentBlocks(context.Background(), m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil {
				out = append(out, *b.Content)
			}
		}
	}
	return out
}

// The core of the move: a message sitting in the mailbox is delivered into the
// thread's history by the same call that is about to read that history.
func TestCallLLMDrain_DeliversQueuedMessageBeforeHistoryRead(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID,
		"actually, check the migration first")

	// Nothing is delivered until something drains.
	require.Empty(t, threadBodies(t, f.h.Repo(), f.chatID, f.chatID),
		"a queued message must not be in history before delivery")

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)
	runInActivity(t, f.h, func(actCtx context.Context) error {
		activityInstance.drainAgentMailbox(actCtx, f.chatID, f.chatID)
		return nil
	})

	bodies := threadBodies(t, f.h.Repo(), f.chatID, f.chatID)
	assert.Contains(t, bodies, "actually, check the migration first",
		"CallLLM must deliver the mailbox into history before it reads history")

	// The row is marked delivered, so the next turn does not re-deliver it.
	queued, err := f.h.Repo().ListQueuedAgentMessagesForThread(ctx, f.chatID)
	require.NoError(t, err)
	assert.Empty(t, queued, "a delivered message must not stay queued")
}

// THE STRANDING FIX. Delivery used to hang off the agent loop's step boundary,
// so a workflow with no `loop` node never delivered at all -- a message queued
// to one sat at status=queued forever while the UI said "waiting for the
// agent's next turn."
//
// Delivery now hangs off CallLLM, which every LLM-calling workflow runs
// regardless of graph shape. This test asserts the property that fixes it:
// delivery needs nothing but a thread and a call.
func TestCallLLMDrain_DeliversForWorkflowsWithNoLoopNode(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	// No loop executor, no step boundary, no iteration -- just the activity, as
	// gsd.yaml and one-ring.yaml would run it.
	queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID, "stranded no more")

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)
	runInActivity(t, f.h, func(actCtx context.Context) error {
		activityInstance.drainAgentMailbox(actCtx, f.chatID, f.chatID)
		return nil
	})

	assert.Contains(t, threadBodies(t, f.h.Repo(), f.chatID, f.chatID), "stranded no more",
		"a loop-less workflow must still receive queued messages")

	queued, err := f.h.Repo().ListQueuedAgentMessagesForThread(ctx, f.chatID)
	require.NoError(t, err)
	assert.Empty(t, queued)
}

// Multiple queued messages arrive in the order the user typed them. Order is
// meaning for conversation turns, and the mailbox is the only thing preserving
// it while the agent is busy.
func TestCallLLMDrain_PreservesQueueOrder(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	for _, body := range []string{"first", "second", "third"} {
		queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID, body)
		// The mailbox orders by created_at; keep them distinguishable.
		time.Sleep(2 * time.Millisecond)
	}

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)
	runInActivity(t, f.h, func(actCtx context.Context) error {
		activityInstance.drainAgentMailbox(actCtx, f.chatID, f.chatID)
		return nil
	})

	bodies := threadBodies(t, f.h.Repo(), f.chatID, f.chatID)
	var seen []string
	for _, b := range bodies {
		switch b {
		case "first", "second", "third":
			seen = append(seen, b)
		}
	}
	assert.Equal(t, []string{"first", "second", "third"}, seen,
		"queued messages must reach history in the order they were sent")
}

// THE LOOP-EXIT GAP.
//
// CallLLM delivers the mailbox BEFORE it reads history, so a message queued
// while the response is streaming is not part of that turn. If the model then
// returns no tool calls, an agent loop that only tests tool_calls exits — on a
// thread still holding an undelivered message, with nothing left to deliver it.
// That is precisely when a user types: watching a long final answer arrive.
//
// hasQueuedAgentMessages is what closes it. CallLLM reports it as
// output.pending_inbox AFTER streaming, and both agent loops OR it into their
// while-condition, so the loop takes one more turn and that turn delivers.
func TestCallLLMDrain_ReportsMessagesQueuedDuringTheTurn(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	// Nothing queued: the loop is free to exit on its own terms.
	var beforeAny bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		beforeAny = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})
	assert.False(t, beforeAny, "an empty mailbox must not force another turn")

	// The user types while the final answer streams.
	queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID, "wait — not that file")

	var afterQueue bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		afterQueue = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})
	assert.True(t, afterQueue,
		"a message that arrived during the turn must keep the loop alive, or it is "+
			"stranded on a thread that is about to stop")

	// Once delivered, the loop is free to exit -- otherwise this term would
	// spin the agent forever on its own delivered message.
	runInActivity(t, f.h, func(actCtx context.Context) error {
		activityInstance.drainAgentMailbox(actCtx, f.chatID, f.chatID)
		return nil
	})

	var afterDrain bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		afterDrain = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})
	assert.False(t, afterDrain,
		"a delivered message must stop forcing turns, or the loop never exits")
}

// The pending-inbox check is scoped to ITS OWN thread. A chat runs several
// threads at once, and a message queued for a sub-agent must not keep the root
// loop turning -- that would spin the root agent on work addressed to someone
// else.
func TestCallLLMDrain_PendingInboxIsScopedToThread(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	// "0" is the legacy sibling thread the fixture also creates.
	const otherThread = "0"
	queueHumanMessage(t, f.h.Repo(), f.chatID, otherThread, "for the other thread")

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	var thisThread, thatThread bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		thisThread = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		thatThread = activityInstance.hasQueuedAgentMessages(actCtx, otherThread)
		return nil
	})

	assert.False(t, thisThread,
		"a message queued for another thread must not keep this loop turning")
	assert.True(t, thatThread, "the addressed thread does see it")
}

// An empty mailbox is the hot path -- it runs before every single LLM call.
// It must be a no-op, not an error and not a write.
func TestCallLLMDrain_EmptyMailboxIsANoOp(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	before := threadBodies(t, f.h.Repo(), f.chatID, f.chatID)

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)
	runInActivity(t, f.h, func(actCtx context.Context) error {
		activityInstance.drainAgentMailbox(actCtx, f.chatID, f.chatID)
		return nil
	})

	assert.Equal(t, before, threadBodies(t, f.h.Repo(), f.chatID, f.chatID),
		"draining an empty mailbox must write nothing")
}

// runInActivity runs fn inside a real Temporal activity context. Activity code
// reads activity.GetLogger, which panics outside one.
func runInActivity(t *testing.T, h *IdempotencyTestHelper, fn func(context.Context) error) {
	t.Helper()
	h.env.RegisterActivity(fn)
	_, err := h.env.ExecuteActivity(fn)
	require.NoError(t, err)
}
