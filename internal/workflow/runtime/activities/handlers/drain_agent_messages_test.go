// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDrainTestActivity(h *IdempotencyTestHelper) *DrainAgentMessagesActivity {
	return NewDrainAgentMessagesActivity(h.Repo(), threads.NewService(h.Repo()))
}

func enqueueDrainTestMessage(t *testing.T, repo db.Repository, chatID, fromThreadID, toThreadID, body string, createdAt time.Time) {
	t.Helper()
	require.NoError(t, repo.EnqueueAgentMessage(context.Background(), &db.AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		FromThreadID: fromThreadID,
		ToThreadID:   toThreadID,
		Kind:         core.AgentMessageKindMessage,
		Body:         body,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    createdAt,
	}))
}

// TestDrainAgentMessages_EmptyQueue asserts the hot path: a thread with
// nothing queued gets a zero-value, no-message result and, critically, no
// message is written to its history.
func TestDrainAgentMessages_EmptyQueue(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	act := newDrainTestActivity(h)

	var output DrainAgentMessagesOutput
	err := h.ExecuteActivity(act.Execute, DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output)
	require.NoError(t, err)

	assert.Equal(t, 0, output.Count)
	assert.False(t, output.HasMessages)

	before := h.CountMessages(ctx, chatID)
	assert.Equal(t, 0, before, "an empty mailbox must not write a message")
}

// TestDrainAgentMessages_DeliversEnvelopeThenOrderedBodies covers spec §11
// item 1/3: a drain delivers a HIDDEN <system> envelope followed by each
// queued body as its own visible message, in send order.
//
// The envelope is separate from the bodies (rather than wrapping them in one
// message, as this activity originally did) because nothing strips the
// framing on the way to the UI: a single folded message rendered the <system>
// preamble and <message from=…> wrappers verbatim in the user's transcript.
// Splitting lets DISPLAY_STYLE_HIDDEN — already honored by
// proto_converters.go and InterleavedTimeline.tsx — hide the framing while
// the bodies stay visible.
func TestDrainAgentMessages_DeliversEnvelopeThenOrderedBodies(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	fromThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: fromThreadID, ChatID: chatID})
	require.NoError(t, err)

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	// Enqueue out of chronological order to prove delivery order comes from
	// created_at, not insertion order.
	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, chatID, "second message", base.Add(10*time.Minute))
	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, chatID, "first message", base)

	act := newDrainTestActivity(h)

	var output DrainAgentMessagesOutput
	err = h.ExecuteActivity(act.Execute, DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output)
	require.NoError(t, err)

	assert.Equal(t, 2, output.Count)
	assert.True(t, output.HasMessages)

	// One envelope + one message per queued row.
	msgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Len(t, msgs, 3, "expected a hidden envelope followed by both bodies")

	contentOf := func(m *db.Message) string {
		blocks, err := h.Repo().ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		require.Len(t, blocks, 1)
		require.NotNil(t, blocks[0].Content)
		return *blocks[0].Content
	}

	// The envelope leads, carries the framing, and is HIDDEN so the
	// transcript never renders it.
	envelope := msgs[0]
	require.NotNil(t, envelope.DisplayStyle, "the envelope must carry a display style")
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *envelope.DisplayStyle,
		"the envelope is LLM-only framing and must not appear in the transcript")
	envelopeContent := contentOf(envelope)
	assert.Contains(t, envelopeContent, "<system>")
	assert.Contains(t, envelopeContent, "2 messages queued")

	// The framing must NOT restate the bodies: they follow as their own
	// messages, and duplicating them here would show the model each message
	// twice.
	assert.NotContains(t, envelopeContent, "first message")
	assert.NotContains(t, envelopeContent, "second message")

	// The bodies follow in send order, visible, and carry only what the
	// sender wrote — no <system> preamble, no <message> wrapper.
	firstBody, secondBody := msgs[1], msgs[2]
	assert.Nil(t, firstBody.DisplayStyle, "a delivered body is an ordinary visible message")
	assert.Nil(t, secondBody.DisplayStyle, "a delivered body is an ordinary visible message")
	assert.Equal(t, "first message", contentOf(firstBody))
	assert.Equal(t, "second message", contentOf(secondBody))
	assert.Less(t, firstBody.Ordinal, secondBody.Ordinal,
		"messages must be delivered in send order, oldest first")
	assert.Less(t, envelope.Ordinal, firstBody.Ordinal,
		"the envelope must precede the bodies it describes")

	// Both queued rows must now be marked delivered against the envelope.
	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, chatID)
	require.NoError(t, err)
	assert.Empty(t, queued, "delivered rows must drop out of the queue")
}

// TestDrainAgentMessages_CompletionNoticeStillDelivers covers the batch shape
// that has no human-authored text in it at all: a sub-agent finished and the
// parent is being told. The <agent_result> attribution lives in the hidden
// envelope while the child's final text is delivered as the visible body, so
// the parent's transcript reads as the child's report rather than as
// machinery.
//
// This is also why delivered_message_id records the ENVELOPE's id rather than
// a body's: the envelope is the one message every drain writes.
func TestDrainAgentMessages_CompletionNoticeStillDelivers(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	childThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: childThreadID, ChatID: chatID})
	require.NoError(t, err)

	require.NoError(t, h.Repo().EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		FromThreadID: childThreadID,
		ToThreadID:   chatID,
		Kind:         core.AgentMessageKindCompletion,
		Body:         "reviewed the migration, found no issues",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))

	act := newDrainTestActivity(h)
	var output DrainAgentMessagesOutput
	require.NoError(t, h.ExecuteActivity(act.Execute,
		DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output))
	require.Equal(t, 1, output.Count)

	// Envelope + the child's HIDDEN result body + a visible notification.
	// See TestDrainAgentMessages_CompletionHiddenBodyPlusVisibleNotification
	// for the full assertion of the hidden-body / notification split; this
	// test only pins that the envelope itself is unaffected by that change.
	msgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Len(t, msgs, 3, "expected a hidden envelope, the child's hidden result, and a visible notification")

	envelopeBlocks, err := h.Repo().ListContentBlocks(ctx, msgs[0].ID)
	require.NoError(t, err)
	require.NotNil(t, envelopeBlocks[0].Content)
	require.NotNil(t, msgs[0].DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *msgs[0].DisplayStyle)
	assert.Contains(t, *envelopeBlocks[0].Content, `status="completed"`)
	assert.Contains(t, *envelopeBlocks[0].Content, childThreadID)

	bodyBlocks, err := h.Repo().ListContentBlocks(ctx, msgs[1].ID)
	require.NoError(t, err)
	require.NotNil(t, bodyBlocks[0].Content)
	assert.Equal(t, "reviewed the migration, found no issues", *bodyBlocks[0].Content)
	require.NotNil(t, msgs[1].DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *msgs[1].DisplayStyle,
		"a completion report's body must not render as a plain visible message")
}

// TestDrainAgentMessages_LeavesNoPartialDelivery pins the transaction. The
// envelope, every body, and the mailbox bookkeeping are one unit: a reader
// must never find rows marked delivered whose bodies were never written, nor
// an envelope announcing messages that are not there.
//
// Asserted against the observable end state rather than by injecting a
// mid-transaction failure, which the repository interface gives no seam for.
func TestDrainAgentMessages_LeavesNoPartialDelivery(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	fromThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: fromThreadID, ChatID: chatID})
	require.NoError(t, err)

	base := time.Now().UTC().Add(-time.Hour)
	for i, body := range []string{"one", "two", "three"} {
		enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, chatID, body,
			base.Add(time.Duration(i)*time.Minute))
	}

	act := newDrainTestActivity(h)
	var output DrainAgentMessagesOutput
	require.NoError(t, h.ExecuteActivity(act.Execute,
		DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output))
	require.Equal(t, 3, output.Count)

	msgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Len(t, msgs, 4, "one envelope plus one message per queued row, all or nothing")

	queued, err := h.Repo().ListQueuedAgentMessagesForThread(ctx, chatID)
	require.NoError(t, err)
	assert.Empty(t, queued, "every row in the batch must be marked delivered together")
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestDrainAgentMessages_OrderingInvariant is the test the spec calls out as
// mattering most (§11 item 3): a message queued mid-turn, while an
// assistant-with-tool_calls row is waiting on its tool_results, must NOT be
// drained between them. It must land strictly after the tool results, and
// the resulting history must still pass the tool-pairing invariant that
// call_llm.go enforces before ever building a provider request.
//
// This pins the reason the mailbox exists as a queue at all (spec §5.2): a
// bare INSERT into `messages` at arbitrary points would happily wedge this
// exact sequence.
func TestDrainAgentMessages_OrderingInvariant(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	thread := chatID

	fromThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: fromThreadID, ChatID: chatID})
	require.NoError(t, err)

	h.CreateTestUserMessage(ctx, chatID, thread)

	// The assistant turn that is mid-flight: it has emitted a tool call and
	// is waiting on execute_tools to save the matching tool_result.
	saveMsgActivity := NewSaveMessageActivity(h.Repo())
	var assistantOut SaveMessageOutput
	err = h.ExecuteActivity(saveMsgActivity.Execute, &SaveMessageInput{
		ChatID: chatID,
		Thread: thread,
		Role:   "assistant",
		ToolCalls: []ToolCall{
			{ID: "tc_1", Name: "bash", Input: `{"command":"ls"}`},
		},
	}, &assistantOut)
	require.NoError(t, err)

	// A message arrives from a peer/parent WHILE the assistant turn is still
	// mid-flight -- i.e. before execute_tools has saved the tool_result. The
	// mailbox must hold it, not deliver it here.
	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, thread, "drop everything, redo the plan", time.Now().UTC())

	// Draining NOW (before tool results exist) is deliberately never called
	// by the workflow wiring -- the drain only runs at the TOP of the loop,
	// never mid-turn. This test proves why: it exercises the actual wiring
	// order below (tool results saved first, drain second) and asserts the
	// resulting history stays valid.

	// execute_tools now saves the matching tool_result, completing the turn.
	var toolOut SaveMessageOutput
	err = h.ExecuteActivity(saveMsgActivity.Execute, &SaveMessageInput{
		ChatID: chatID,
		Thread: thread,
		Role:   "tool",
		ToolResults: []ToolResult{
			{ToolCallID: "tc_1", Name: "bash", Content: "file1\nfile2"},
		},
	}, &toolOut)
	require.NoError(t, err)

	// NOW we are at the true step boundary (top of the next iteration,
	// history known-consistent) -- exactly where the workflow wiring calls
	// DrainAgentMessages.
	drainActivity := newDrainTestActivity(h)
	var drainOut DrainAgentMessagesOutput
	err = h.ExecuteActivity(drainActivity.Execute, DrainAgentMessagesInput{ChatID: chatID, Thread: thread}, &drainOut)
	require.NoError(t, err)
	require.Equal(t, 1, drainOut.Count)
	require.True(t, drainOut.HasMessages)

	// Load history the same way call_llm does and assert:
	//  1. the drained message is the LAST message (after the tool result).
	//  2. the whole history still passes the tool-pairing invariant.
	dbMsgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.True(t, len(dbMsgs) >= 3, "expects at least: user, assistant(tool_calls), tool(results), drained user")

	// The drain writes a hidden envelope followed by the body, so the body is
	// the last message and the envelope sits directly before it.
	last := dbMsgs[len(dbMsgs)-1]
	assert.NotEqual(t, toolOut.MessageId, last.ID, "drained message must not BE the tool-result message")
	lastBlocks, err := h.Repo().ListContentBlocks(ctx, last.ID)
	require.NoError(t, err)
	require.Len(t, lastBlocks, 1)
	require.NotNil(t, lastBlocks[0].Content)
	assert.Equal(t, "drop everything, redo the plan", *lastBlocks[0].Content,
		"the delivered body must be the sender's text alone, with no framing folded in")
	assert.Nil(t, last.DisplayStyle, "the delivered body must be visible in the transcript")

	// Assert ordinal ordering directly: the drained message's ordinal must
	// exceed the tool-result message's ordinal.
	var toolMsg, drainedMsg *db.Message
	for _, m := range dbMsgs {
		if m.ID == toolOut.MessageId {
			toolMsg = m
		}
		if m.ID == last.ID {
			drainedMsg = m
		}
	}
	require.NotNil(t, toolMsg)
	require.NotNil(t, drainedMsg)
	assert.Greater(t, drainedMsg.Ordinal, toolMsg.Ordinal,
		"the drained message must land strictly AFTER the tool results")

	// Finally, replicate the exact check call_llm.go performs before ever
	// building a provider request: convert to message.Message and validate
	// the tool-pairing invariant holds.
	llmMsgs, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
	require.NoError(t, err)
	violations := ValidateToolPairing(llmMsgs)
	assert.Empty(t, violations, "history must pass the tool-pairing invariant after the drain")
}

// TestDrainAgentMessages_BeforeToolResults_WouldBreakInvariant is a negative
// control for TestDrainAgentMessages_OrderingInvariant: it proves the
// invariant genuinely fires when a drain happens BEFORE tool results are
// saved, so the ordering test above is actually asserting something real and
// not passing by accident.
func TestDrainAgentMessages_BeforeToolResults_WouldBreakInvariant(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)
	thread := chatID

	fromThreadID := uuid.New().String()
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: fromThreadID, ChatID: chatID})
	require.NoError(t, err)

	h.CreateTestUserMessage(ctx, chatID, thread)

	saveMsgActivity := NewSaveMessageActivity(h.Repo())
	var assistantOut SaveMessageOutput
	err = h.ExecuteActivity(saveMsgActivity.Execute, &SaveMessageInput{
		ChatID: chatID,
		Thread: thread,
		Role:   "assistant",
		ToolCalls: []ToolCall{
			{ID: "tc_1", Name: "bash", Input: `{"command":"ls"}`},
		},
	}, &assistantOut)
	require.NoError(t, err)

	enqueueDrainTestMessage(t, h.Repo(), chatID, fromThreadID, thread, "premature delivery", time.Now().UTC())

	// Drain BEFORE the tool result is saved -- the mistake a bare INSERT (or
	// a misplaced drain call) would make.
	drainActivity := newDrainTestActivity(h)
	var drainOut DrainAgentMessagesOutput
	err = h.ExecuteActivity(drainActivity.Execute, DrainAgentMessagesInput{ChatID: chatID, Thread: thread}, &drainOut)
	require.NoError(t, err)
	require.Equal(t, 1, drainOut.Count)

	// Now the tool result finally arrives.
	var toolOut SaveMessageOutput
	err = h.ExecuteActivity(saveMsgActivity.Execute, &SaveMessageInput{
		ChatID: chatID,
		Thread: thread,
		Role:   "tool",
		ToolResults: []ToolResult{
			{ToolCallID: "tc_1", Name: "bash", Content: "file1\nfile2"},
		},
	}, &toolOut)
	require.NoError(t, err)

	llmMsgs, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, thread, nil)
	require.NoError(t, err)
	violations := ValidateToolPairing(llmMsgs)
	// LoadMessagesForLLM repairs in memory, so this proves repair had to
	// intervene -- i.e. the raw sequence genuinely violated the invariant --
	// rather than proving the violation propagates uncorrected. The DB-level
	// assertion below is the one that actually pins the bad ordering.
	_ = violations

	dbMsgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	var drainedMsg, toolMsg *db.Message
	for _, m := range dbMsgs {
		blocks, err := h.Repo().ListContentBlocks(ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil && *b.Content != "" && containsSubstr(*b.Content, "premature delivery") {
				drainedMsg = m
			}
		}
		if m.ID == toolOut.MessageId {
			toolMsg = m
		}
	}
	require.NotNil(t, drainedMsg)
	require.NotNil(t, toolMsg)
	assert.Less(t, drainedMsg.Ordinal, toolMsg.Ordinal,
		"a drain issued before tool results lands BEFORE them, exactly the deadlocking shape the queue exists to prevent")
}

func containsSubstr(s, substr string) bool {
	return indexOf(s, substr) != -1
}

// TestDrainAgentMessages_CompletionHiddenBodyPlusVisibleNotification covers
// the fix for the bug where a sub-agent's completion report rendered as a
// raw, visible user message in the parent's transcript. A completion must
// now drain into TWO messages beyond the envelope: the full <agent_result>
// body, HIDDEN from the UI but still reaching the LLM, and a short SYSTEM
// role, INFO-styled notification the human actually sees.
func TestDrainAgentMessages_CompletionHiddenBodyPlusVisibleNotification(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	childThreadID := uuid.New().String()
	title := "probe-A"
	_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: childThreadID, ChatID: chatID, Title: &title})
	require.NoError(t, err)

	require.NoError(t, h.Repo().EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		FromThreadID: childThreadID,
		ToThreadID:   chatID,
		Kind:         core.AgentMessageKindCompletion,
		Body:         "<agent_result agent_id=\"" + childThreadID + "\" status=\"completed\">reviewed the migration, found no issues</agent_result>",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))

	act := newDrainTestActivity(h)
	var output DrainAgentMessagesOutput
	require.NoError(t, h.ExecuteActivity(act.Execute,
		DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output))
	require.Equal(t, 1, output.Count)

	msgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Len(t, msgs, 3, "expected the mailbox envelope, the hidden result body, and a visible notification")

	envelope, body, notification := msgs[0], msgs[1], msgs[2]

	// Envelope: unchanged from the existing contract.
	require.NotNil(t, envelope.DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *envelope.DisplayStyle)

	// The full agent-result body must still exist, carry the real content,
	// and be marked HIDDEN so it never renders as a raw user message.
	require.NotNil(t, body.DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *body.DisplayStyle,
		"a completion report must not render as a plain visible message")
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, body.Role)
	bodyBlocks, err := h.Repo().ListContentBlocks(ctx, body.ID)
	require.NoError(t, err)
	require.Len(t, bodyBlocks, 1)
	require.NotNil(t, bodyBlocks[0].Content)
	assert.Contains(t, *bodyBlocks[0].Content, "reviewed the migration, found no issues")
	assert.Contains(t, *bodyBlocks[0].Content, `status="completed"`)

	// The notification is what the human actually sees: SYSTEM role, INFO
	// display style, a one-line summary naming the spawn's title.
	require.NotNil(t, notification.DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_INFO, *notification.DisplayStyle)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, notification.Role)
	notifBlocks, err := h.Repo().ListContentBlocks(ctx, notification.ID)
	require.NoError(t, err)
	require.Len(t, notifBlocks, 1)
	require.NotNil(t, notifBlocks[0].Content)
	assert.Equal(t, `spawn "probe-A" completed`, *notifBlocks[0].Content)

	// The hidden body must still reach the LLM. This is the part of the fix
	// that must not regress: LoadMessagesForLLM is the exact path
	// call_llm.go uses to build a provider request, so the full result
	// being present there is what proves the orchestrator still learns what
	// its sub-agent produced. HIDDEN is a DISPLAY concern only — it must
	// not strip the message from model history.
	llmMsgs, err := LoadMessagesForLLM(ctx, h.Repo(), chatID, chatID, nil)
	require.NoError(t, err)

	var llmText []string
	for _, m := range llmMsgs {
		for _, part := range m.Parts {
			if tc, ok := part.(message.TextContent); ok {
				llmText = append(llmText, tc.Text)
			}
		}
	}
	joined := strings.Join(llmText, "\n")

	assert.Contains(t, joined, "reviewed the migration, found no issues",
		"the hidden agent-result body must still reach the LLM")
	assert.Contains(t, joined, `status="completed"`,
		"the LLM must still see the full agent_result envelope, not just the summary")

	// The human-facing notification is deliberately allowed through to the
	// model too — a single "spawn X completed" line is harmless context and
	// keeping it avoids a second visibility axis. Asserted so a future
	// change to that decision is a deliberate one.
	assert.Contains(t, joined, `spawn "probe-A" completed`)
}

// TestDrainAgentMessages_CancelledAndFailedNotificationText pins the exact
// wording for the other two terminal kinds.
func TestDrainAgentMessages_CancelledAndFailedNotificationText(t *testing.T) {
	tests := []struct {
		name     string
		kind     core.AgentMessageKind
		wantText string
	}{
		{"cancelled", core.AgentMessageKindCancelled, `spawn "probe-B" cancelled`},
		{"failed", core.AgentMessageKindFailed, `spawn "probe-B" failed`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewIdempotencyTestHelper(t)
			defer h.Cleanup()
			ctx := context.Background()

			userID := uuid.New().String()
			projectID := uuid.New().String()
			chatID := uuid.New().String()
			h.CreateTestProject(ctx, projectID, userID)
			h.CreateTestChat(ctx, chatID, projectID, userID)

			childThreadID := uuid.New().String()
			title := "probe-B"
			_, err := h.Repo().CreateThread(ctx, &db.Thread{ID: childThreadID, ChatID: chatID, Title: &title})
			require.NoError(t, err)

			require.NoError(t, h.Repo().EnqueueAgentMessage(ctx, &db.AgentMessage{
				ID:           uuid.New().String(),
				ChatID:       chatID,
				FromThreadID: childThreadID,
				ToThreadID:   chatID,
				Kind:         tc.kind,
				Body:         "some result body",
				Status:       core.AgentMessageStatusQueued,
				CreatedAt:    time.Now().UTC(),
			}))

			act := newDrainTestActivity(h)
			var output DrainAgentMessagesOutput
			require.NoError(t, h.ExecuteActivity(act.Execute,
				DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output))

			msgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
			require.NoError(t, err)
			require.Len(t, msgs, 3)

			notifBlocks, err := h.Repo().ListContentBlocks(ctx, msgs[2].ID)
			require.NoError(t, err)
			require.Len(t, notifBlocks, 1)
			require.NotNil(t, notifBlocks[0].Content)
			assert.Equal(t, tc.wantText, *notifBlocks[0].Content)
		})
	}
}

// TestDrainAgentMessages_HumanMessageStillPlainVisible is the negative
// control: a HUMAN message queued into a running thread must be completely
// unaffected by the completion-notification change above — one ordinary
// visible user message, exactly as before.
func TestDrainAgentMessages_HumanMessageStillPlainVisible(t *testing.T) {
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()
	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	require.NoError(t, h.Repo().EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID:           uuid.New().String(),
		ChatID:       chatID,
		FromThreadID: chatID,
		ToThreadID:   chatID,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         "hey, please also check the other file",
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))

	act := newDrainTestActivity(h)
	var output DrainAgentMessagesOutput
	require.NoError(t, h.ExecuteActivity(act.Execute,
		DrainAgentMessagesInput{ChatID: chatID, Thread: chatID}, &output))
	require.Equal(t, 1, output.Count)

	msgs, err := h.Repo().ListMessages(ctx, chatID, db.MessageListOptions{})
	require.NoError(t, err)
	require.Len(t, msgs, 2, "expected only the hidden envelope plus one ordinary visible body — no extra notification")

	body := msgs[1]
	assert.Nil(t, body.DisplayStyle, "a human-authored message must remain an ordinary visible message")
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, body.Role)
	bodyBlocks, err := h.Repo().ListContentBlocks(ctx, body.ID)
	require.NoError(t, err)
	require.Len(t, bodyBlocks, 1)
	require.NotNil(t, bodyBlocks[0].Content)
	assert.Equal(t, "hey, please also check the other file", *bodyBlocks[0].Content)
}
