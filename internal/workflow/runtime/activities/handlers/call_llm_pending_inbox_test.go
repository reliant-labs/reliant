// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// THE WEDGE (chat 2b4c8c20, thread 8c48ab59, 48 identical failures)
//
// A text-only assistant turn — text, no tool calls — is a COMPLETE turn. The
// agent loop must exit and yield to the user. Instead it re-entered call_llm
// because pending_inbox was true, and pending_inbox was set from
// `streamInterrupted` rather than from any mailbox check:
//
//	PendingInbox: streamInterrupted,        // call_llm.go, before this change
//
// With an empty mailbox the drain delivered nothing, history still ended with
// that assistant message, the end-of-history guard returned an error, and the
// retry ladder reproduced it forever. Verified against the live database: the
// wedged thread's mailbox held exactly one row at status=2 (already delivered)
// and the sibling wedged thread's mailbox was completely empty — pending_inbox
// was lying in both cases.
//
// These tests pin both halves of the fix:
//
//   - PREVENTION: pending_inbox means "a real queued message exists and the
//     next turn will deliver it", so re-entering call_llm genuinely ends
//     history with a USER message. Empty mailbox ⇒ false ⇒ the loop exits.
//   - RECOVERY: a thread whose tail is ALREADY a text-only assistant message
//     yields cleanly instead of erroring, so it can resume on its next turn.
//     The pre-existing blockless recovery only handled the ZERO-content-block
//     shape, which is exactly why this case slipped through.

// PREVENTION. The probe is the sole source of truth, and an empty mailbox must
// report false — otherwise the loop re-enters on a thread with nothing to
// deliver, which is the wedge.
func TestPendingInbox_EmptyMailboxReportsFalse(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	var pending bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		pending = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})

	assert.False(t, pending,
		"an empty mailbox must report no pending inbox — reporting true is what "+
			"made the agent loop re-enter call_llm forever on a thread with nothing to deliver")
}

// A delivered row is not a pending one. The wedged thread's mailbox held
// exactly this: one row at status=2. If that counted as pending, the loop would
// re-enter on a message the model has already seen.
func TestPendingInbox_DeliveredRowIsNotPending(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()

	queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID, "already read this")

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	// Deliver it, exactly as a turn's drain would.
	runInActivity(t, f.h, func(actCtx context.Context) error {
		activityInstance.drainAgentMailbox(actCtx, f.chatID, f.chatID)
		return nil
	})
	queued, err := f.h.Repo().ListQueuedAgentMessagesForThread(ctx, f.chatID)
	require.NoError(t, err)
	require.Empty(t, queued, "precondition: the drain must have claimed the row")

	var pending bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		pending = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})

	assert.False(t, pending,
		"a row already delivered must not count as a pending inbox — this is the exact "+
			"state of the wedged thread's mailbox (one row, status=2)")
}

// The other half of the invariant: a REAL queued message must still report
// true. The fix must not buy wedge-immunity by stranding messages — that is the
// regression the pending_inbox term was introduced to prevent.
func TestPendingInbox_RealQueuedMessageReportsTrue(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID, "actually, check the migration first")

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	var pending bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		pending = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})

	assert.True(t, pending,
		"a genuinely queued message must earn another turn, or it is stranded — "+
			"call_llm is the only deliverer")
}

// THE INTERRUPT RACE, resolved.
//
// An interrupted turn reaches the probe with its context ALREADY CANCELLED
// (verified in the incident logs: the same cancellation that killed the stream
// also failed an unrelated settings read with "context canceled"). So the probe
// must run detached, or it would answer false for the very message the
// interrupt was issued to deliver — trading the wedge for a stranded message.
//
// This is the test that distinguishes a correct fix from one that merely stops
// the looping: with a live queued message and a dead context, the answer must
// still be true.
func TestPendingInbox_SurvivesCancelledContext(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	queueHumanMessage(t, f.h.Repo(), f.chatID, f.chatID, "stop, this changes things")

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	var pending bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		cancelled, cancel := context.WithCancel(actCtx)
		cancel() // exactly the state an interrupted turn arrives in

		probeCtx, cancelProbe := context.WithTimeout(
			context.WithoutCancel(cancelled), 5*time.Second)
		defer cancelProbe()
		pending = activityInstance.hasQueuedAgentMessages(probeCtx, f.chatID)
		return nil
	})

	assert.True(t, pending,
		"an interrupted turn's probe must survive its own cancellation — otherwise the "+
			"message the user interrupted to send is stranded, which is the regression "+
			"the pending_inbox term exists to prevent")
}

// RECOVERY. The wedged tail shape: an assistant message with ONE text block and
// no tool calls. It is NOT blockless, so dropBlocklessAssistantMessages leaves
// it alone — correctly, since discarding it would destroy real model output the
// user already read.
//
// That means recovery cannot come from filtering. It has to come from call_llm
// treating an assistant tail as a completed turn and yielding, which is what
// the guard now does.
func TestRecovery_TextOnlyAssistantTailIsNotBlockless(t *testing.T) {
	msgs := []message.Message{
		userWithText("u1", "go ahead"),
		assistantWithText("a1", "While that runs, let me review my full diff and check lint/typecheck."),
	}

	kept, dropped := dropBlocklessAssistantMessages(msgs)

	require.Equal(t, 0, dropped,
		"a text-only assistant message carries real output and must never be dropped")
	require.Len(t, kept, 2)
	assert.Equal(t, message.Assistant, kept[len(kept)-1].Role,
		"the history still ends with the assistant message — so recovery must come from "+
			"call_llm yielding on that tail, not from filtering it away")
}

// saveTextOnlyAssistantMessage writes the wedged tail shape: an ASSISTANT
// message with exactly one text content block and no tool calls. Mirrors
// CreateTestUserMessageWithText, which is how the sibling half of the
// conversation is written.
func saveTextOnlyAssistantMessage(t *testing.T, h *IdempotencyTestHelper, chatID, threadID, text string) string {
	t.Helper()
	ctx := context.Background()

	msgID := uuid.New().String()
	ordinal, err := h.Repo().GetNextOrdinal(ctx, threadID)
	require.NoError(t, err)
	seq, err := h.Repo().GetNextSeq(ctx, chatID, threadID)
	require.NoError(t, err)

	require.NoError(t, h.Repo().CreateMessage(ctx, &db.Message{
		ID:              msgID,
		ChatID:          chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal:         ordinal,
		Seq:             seq,
		ThreadID:        threadID,
		ContextWindowID: chatID + ":" + threadID + ":0",
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}))

	require.NoError(t, h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
		ID:        uuid.New().String(),
		MessageID: msgID,
		BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
		Position:  0,
		Content:   ptr.Of(text),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}))

	return msgID
}

// RECOVERY, end to end. A thread whose tail is a text-only assistant message
// with an empty mailbox must yield instead of erroring.
//
// Before the fix this returned:
//
//	failed to stream LLM response: conversation history ends with assistant
//	message after all transformations (...)
//
// which the retry ladder reproduced forever — the chat could never advance
// again without hand-editing the database. Now the turn ends cleanly with no
// tool calls and no pending inbox, so the agent loop's while-condition is false
// on every term and it exits, yielding to the user. The user's next message
// becomes the new tail and the thread is live again.
func TestRecovery_AssistantTailYieldsInsteadOfWedging(t *testing.T) {
	resolver := mockLLMDriverResolver()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()

	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)

	// Reproduce the wedged tail: a user turn, then a text-only assistant reply
	// with no tool calls — and nothing queued behind it.
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	saveTextOnlyAssistantMessage(t, h, chat.ID, chat.ID,
		"While that runs, let me review my full diff and check lint/typecheck.")

	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, chat.ID)
	require.NoError(t, err)
	require.Empty(t, queued, "precondition: the wedged thread's mailbox is empty")

	activityInstance := NewCallLLMActivity(h.Repo(), nil, nil, &staticConfigProvider{}, resolver, nil)

	var output CallLLMOutput
	err = h.ExecuteActivity(activityInstance.Execute,
		callLLMInput(chat.ID, chat.ID, "mock-model"), &output)

	require.NoError(t, err,
		"a thread whose tail is a text-only assistant message must YIELD, not error — "+
			"erroring is what wedged chat 2b4c8c20 permanently")

	// Every term of the agent loop's while-condition must be false, or the loop
	// re-enters and the wedge is merely relocated.
	assert.Empty(t, output.ToolCalls,
		"a yielded turn requests no tools, so the loop's tool_calls term is false")
	assert.False(t, output.PendingInbox,
		"the mailbox is empty, so pending_inbox must be false and the loop must exit")
}

// SCOPING — THE MAIN REGRESSION RISK.
//
// A text-only assistant tail is the NORMAL, HEALTHY resting state of a finished
// turn awaiting the user. Measured across the live database: 1,200 threads have
// that tail and only 2 were wedged. So the shape alone is emphatically NOT the
// defect, and any repair that keys off it would fire on ~1,198 healthy idle
// threads.
//
// This fix does not key off the shape. The distinguishing condition is the
// COMBINATION — a text-only assistant tail AND the loop re-entering call_llm
// AND an empty mailbox — i.e. pending_inbox claimed a delivery and the drain
// delivered nothing. Both halves key off exactly that:
//
//   - Prevention removes the false claim at the source (pending_inbox now comes
//     only from a real mailbox probe), so the loop never re-enters on an empty
//     mailbox in the first place.
//   - Recovery lives INSIDE call_llm, on the path taken only when something has
//     ALREADY re-entered with an assistant tail. An idle thread is not running
//     call_llm at all — nothing executes this code for it.
//
// This test pins the healthy case: an idle thread's history is untouched by the
// repair pass, so it neither gains a turn nor loses its last message.
func TestScoping_HealthyIdleTextOnlyTailIsUntouched(t *testing.T) {
	tail := "Done — the migration is applied and the tests pass."
	msgs := []message.Message{
		userWithText("u1", "apply the migration"),
		assistantWithText("a1", tail),
	}

	kept, dropped := dropBlocklessAssistantMessages(msgs)

	require.Equal(t, 0, dropped,
		"the resting state of 1,198 healthy idle threads must not be treated as damage")
	require.Len(t, kept, 2, "an idle thread's history must survive the repair pass intact")
	assert.Equal(t, tail, kept[1].Content().String(),
		"the assistant's final answer is what the user is reading — it must not be rewritten")
}

// The same scoping fact, stated on the flag the agent loop actually reads.
//
// An idle thread with a text-only tail and an empty mailbox reports
// pending_inbox=false, so every term of the while-condition is false and the
// loop stays exited. Nothing about this fix pushes such a thread into an extra
// turn — the flag is the only lever that could, and it reads false.
func TestScoping_IdleThreadWithEmptyMailboxEarnsNoExtraTurn(t *testing.T) {
	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()

	activityInstance := NewCallLLMActivity(f.h.Repo(), nil, nil, &staticConfigProvider{}, nil, nil)

	var pending bool
	runInActivity(t, f.h, func(actCtx context.Context) error {
		pending = activityInstance.hasQueuedAgentMessages(actCtx, f.chatID)
		return nil
	})

	assert.False(t, pending,
		"an idle thread with nothing queued must earn no extra turn — pending_inbox is "+
			"the only term that could grant one, and it must read false")
}
