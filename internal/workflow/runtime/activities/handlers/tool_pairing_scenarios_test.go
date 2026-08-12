// Copyright (c) 2025 Reliant Labs
//
// Scenario coverage for the tool-call/tool-result pairing invariant.
//
// HARD REQUIREMENT: we never respond to the LLM with tool calls that lack
// matching results. A violation is not a soft failure — the provider rejects the
// request, every retry replays the same history, and the chat is permanently
// wedged.
//
// Each test below reproduces a real way a conversation can end up with a
// dangling tool call, drives it through the actual prompt-assembly boundary
// (LoadMessagesForLLM), and asserts the invariant holds on what comes out. The
// assertion is always ValidateToolPairing — the same definition the production
// code enforces — so these tests cannot drift from the rule they protect.
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireInvariantHolds is the single assertion every scenario ends with.
func requireInvariantHolds(t *testing.T, msgs []message.Message, scenario string) {
	t.Helper()
	if violations := ValidateToolPairing(msgs); len(violations) > 0 {
		t.Fatalf("tool-pairing invariant violated after %s: %s\n(this history would wedge the conversation at the provider)",
			scenario, summarizeViolations(violations))
	}
}

// findResultFor returns the tool_result for a call id, if the history has one.
func findResultFor(msgs []message.Message, toolCallID string) (message.ToolResult, bool) {
	for _, m := range msgs {
		for _, tr := range m.ToolResults() {
			if tr.ToolCallID == toolCallID {
				return tr, true
			}
		}
	}
	return message.ToolResult{}, false
}

// createToolCallBlockOnMessage attaches an extra tool_call block to an existing
// assistant message, for modelling a multi-tool batch in one turn.
func createToolCallBlockOnMessage(t *testing.T, repo db.Repository, messageID, toolCallID, toolName, toolInput string, position int) {
	t.Helper()
	require.NoError(t, repo.CreateContentBlock(context.Background(), &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  messageID,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
		ToolName:   &toolName,
		ToolInput:  &toolInput,
		ToolCallID: &toolCallID,
		Position:   position,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}))
}

// createToolResultBlockOnMessage attaches an extra tool_result block to an
// existing tool message, for a partially-answered batch.
func createToolResultBlockOnMessage(t *testing.T, repo db.Repository, messageID, toolCallID, content string, isError bool, position int) {
	t.Helper()
	require.NoError(t, repo.CreateContentBlock(context.Background(), &db.MessageContentBlock{
		ID:         uuid.New().String(),
		MessageID:  messageID,
		BlockType:  reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT,
		Content:    &content,
		ToolCallID: &toolCallID,
		IsError:    &isError,
		IsComplete: true,
		Position:   position,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}))
}

// TestToolPairing_WorkflowCancelledMidToolExecution: the workflow is cancelled
// after the assistant message with its tool_call is persisted but before any
// result is written. This is the single most common orphan source.
func TestToolPairing_WorkflowCancelledMidToolExecution(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "list the files")
	createMessageWithToolCall(t, repo, chatID, threadID, cwID, 2, "call_cancelled", "bash", `{"command":"ls -la"}`)
	// Cancellation: no tool message is ever written.

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "workflow cancelled mid-tool-execution")

	result, ok := findResultFor(msgs, "call_cancelled")
	require.True(t, ok, "the cancelled call must still reach the LLM with some result")
	assert.True(t, result.IsError, "an interrupted call's result must be marked as an error")
	assert.Contains(t, result.Content, "interrupted",
		"the model must be told the outcome is unknown so it re-verifies side effects")
}

// TestToolPairing_PauseDuringToolExecution: the chat is paused mid-execution and
// then read again (a resumed run, or the user simply reopening the chat). The
// read path must not deadlock the resumed conversation.
func TestToolPairing_PauseDuringToolExecution(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "run the migration")
	createMessageWithToolCall(t, repo, chatID, threadID, cwID, 2, "call_paused", "bash", `{"command":"make migrate"}`)

	// Read twice: pausing and resuming means this history is assembled more than
	// once, and repeated reads must stay stable rather than compounding repairs.
	first, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, first, "pause during tool execution (first read)")

	second, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, second, "pause during tool execution (second read)")

	assert.Equal(t, len(first), len(second),
		"re-reading a paused conversation must be stable, not grow a repair message each time")
}

// TestToolPairing_CrashBetweenAssistantAndResults: the process dies between
// persisting the assistant message and persisting the results, so CleanupActivity
// never runs. Nothing at rest fixes this; the boundary must.
func TestToolPairing_CrashBetweenAssistantAndResults(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "do three things")
	asst := createMessageWithToolCall(t, repo, chatID, threadID, cwID, 2, "call_crash_1", "bash", `{"command":"a"}`)
	createToolCallBlockOnMessage(t, repo, asst.ID, "call_crash_2", "view", `{"file_path":"b"}`, 1)
	createToolCallBlockOnMessage(t, repo, asst.ID, "call_crash_3", "grep", `{"pattern":"c"}`, 2)
	// Crash: no tool message at all, for any of the three calls.

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "crash between saving assistant message and tool results")

	for _, id := range []string{"call_crash_1", "call_crash_2", "call_crash_3"} {
		_, ok := findResultFor(msgs, id)
		assert.True(t, ok, "every call in the crashed batch needs a result, missing %s", id)
	}
}

// TestToolPairing_PartialBatchSomeToolsSucceeded: a multi-tool turn where only
// some calls got results. The successful results must survive untouched while
// the unanswered ones are filled in.
func TestToolPairing_PartialBatchSomeToolsSucceeded(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "run the batch")
	asst := createMessageWithToolCall(t, repo, chatID, threadID, cwID, 2, "call_ok", "bash", `{"command":"echo ok"}`)
	createToolCallBlockOnMessage(t, repo, asst.ID, "call_failed", "view", `{"file_path":"/nope"}`, 1)
	createToolCallBlockOnMessage(t, repo, asst.ID, "call_never_ran", "grep", `{"pattern":"x"}`, 2)

	// Only two of the three produced results before the run ended.
	toolMsg := createMessageWithToolResult(t, repo, chatID, threadID, cwID, 3, "call_ok", "ok output")
	createToolResultBlockOnMessage(t, repo, toolMsg.ID, "call_failed", "file not found", true, 1)

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "batch where some tools succeeded and others did not")

	okResult, found := findResultFor(msgs, "call_ok")
	require.True(t, found)
	assert.Equal(t, "ok output", okResult.Content, "a real success result must not be overwritten by repair")
	assert.False(t, okResult.IsError)

	failedResult, found := findResultFor(msgs, "call_failed")
	require.True(t, found)
	assert.Equal(t, "file not found", failedResult.Content, "a real error result must be preserved verbatim")
	assert.True(t, failedResult.IsError)

	neverRan, found := findResultFor(msgs, "call_never_ran")
	require.True(t, found, "the unanswered call must be filled in")
	assert.True(t, neverRan.IsError)
	assert.Contains(t, neverRan.Content, "interrupted")
}

// TestToolPairing_ForkBetweenCallAndResult is the case that justifies keeping an
// in-memory repair at all. A branch forks at the assistant message, so the child
// inherits the tool_call but NOT the tool message that answered it. Those parent
// rows belong to another conversation and must not be rewritten to fix the child.
func TestToolPairing_ForkBetweenCallAndResult(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	parentThread, parentCW := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, parentThread, parentCW, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "list files")
	createMessageWithToolCall(t, repo, chatID, parentThread, parentCW, 2, "call_forked", "bash", `{"command":"ls"}`)
	// The parent DID get its result, at ordinal 3.
	createMessageWithToolResult(t, repo, chatID, parentThread, parentCW, 3, "call_forked", "file1.txt")

	// Branch cuts at ordinal 2: the child inherits the call, not the answer.
	forkOrdinal := int64(2)
	childThread, childCW := createThreadWithContextWindow(t, repo, chatID, &parentThread, &forkOrdinal, &parentCW, 0)
	createMessageWithParts(t, repo, chatID, childThread, childCW, 4, reliantv1.MessageRole_MESSAGE_ROLE_USER, "actually, do something else")

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, childThread, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "fork between a tool call and its result")

	_, found := findResultFor(msgs, "call_forked")
	assert.True(t, found, "the inherited orphaned call must be answered in the child's assembled history")

	// The parent must be untouched: its own history still resolves to the REAL
	// result, not a synthetic one written by the child's read.
	parentMsgs, err := LoadMessagesForLLM(ctx, repo, chatID, parentThread, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, parentMsgs, "parent thread after a child forked mid-pair")

	parentResult, found := findResultFor(parentMsgs, "call_forked")
	require.True(t, found)
	assert.Equal(t, "file1.txt", parentResult.Content,
		"repairing the child must not overwrite the parent's real tool result")
}

// TestToolPairing_ForkAtResultKeepsRealResult: forking AFTER the result must
// inherit the genuine output, never a synthetic stand-in.
func TestToolPairing_ForkAtResultKeepsRealResult(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	parentThread, parentCW := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, parentThread, parentCW, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "list files")
	createMessageWithToolCall(t, repo, chatID, parentThread, parentCW, 2, "call_kept", "bash", `{"command":"ls"}`)
	createMessageWithToolResult(t, repo, chatID, parentThread, parentCW, 3, "call_kept", "real output here")

	forkOrdinal := int64(3) // after the result
	childThread, childCW := createThreadWithContextWindow(t, repo, chatID, &parentThread, &forkOrdinal, &parentCW, 0)
	createMessageWithParts(t, repo, chatID, childThread, childCW, 4, reliantv1.MessageRole_MESSAGE_ROLE_USER, "continue")

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, childThread, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "fork after a completed tool pair")

	result, found := findResultFor(msgs, "call_kept")
	require.True(t, found)
	assert.Equal(t, "real output here", result.Content, "a completed pair must be inherited intact")
	assert.False(t, result.IsError)
}

// TestToolPairing_BackgroundedTool documents the deliberate answer for a tool
// that resolves out of band.
//
// A backgrounded tool does NOT produce an orphan: ExecuteTools returns an inline
// placeholder result the moment it backgrounds the call (execute_tools.go), so
// the pair closes immediately and the conversation keeps moving. The eventual
// real output arrives later as its own message. That is the correct behaviour —
// the alternative, leaving the call unanswered until the background work lands,
// would block the turn on something explicitly made non-blocking.
func TestToolPairing_BackgroundedTool(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "start the dev server")
	createMessageWithToolCall(t, repo, chatID, threadID, cwID, 2, "call_bg", "bash", `{"command":"npm run dev"}`)
	// The placeholder ExecuteTools writes when it backgrounds a call.
	createMessageWithToolResult(t, repo, chatID, threadID, cwID, 3, "call_bg",
		"Command running in background with ID: bg_1")

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "backgrounded tool")

	result, found := findResultFor(msgs, "call_bg")
	require.True(t, found)
	assert.Contains(t, result.Content, "background",
		"the backgrounded placeholder must survive rather than be replaced by an interrupted stub")
	assert.False(t, result.IsError, "backgrounding is a normal outcome, not an error")
}

// TestToolPairing_OrphanedResultWithNoCall: the mirror-image violation. A
// tool_result whose tool_use is absent is rejected by providers just as a
// dangling call is, so it must be dropped rather than forwarded.
func TestToolPairing_OrphanedResultWithNoCall(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "hello")
	// A result with no corresponding call anywhere in the history.
	createMessageWithToolResult(t, repo, chatID, threadID, cwID, 2, "call_ghost", "output with no call")

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "orphaned tool_result with no matching call")

	_, found := findResultFor(msgs, "call_ghost")
	assert.False(t, found, "a result with no matching tool_use must be dropped, not sent to the provider")
}

// TestToolPairing_HealthyConversationIsUnchanged is the control: a conversation
// that already satisfies the invariant must pass through untouched. Without this,
// the tests above could be satisfied by a repair pass that rewrites everything.
func TestToolPairing_HealthyConversationIsUnchanged(t *testing.T) {
	repo, cleanup := setupTestRepoWithCleanup(t)
	defer cleanup()
	ctx := context.Background()

	chatID := createTestChatForConversation(t, repo)
	threadID, cwID := createThreadWithContextWindow(t, repo, chatID, nil, nil, nil, 0)

	createMessageWithParts(t, repo, chatID, threadID, cwID, 1, reliantv1.MessageRole_MESSAGE_ROLE_USER, "list files")
	createMessageWithToolCall(t, repo, chatID, threadID, cwID, 2, "call_1", "bash", `{"command":"ls"}`)
	createMessageWithToolResult(t, repo, chatID, threadID, cwID, 3, "call_1", "file1.txt\nfile2.txt")
	createMessageWithParts(t, repo, chatID, threadID, cwID, 4, reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT, "Two files.")

	msgs, err := LoadMessagesForLLM(ctx, repo, chatID, threadID, nil)
	require.NoError(t, err)
	requireInvariantHolds(t, msgs, "healthy conversation")

	assert.Len(t, msgs, 4, "a valid conversation must not gain or lose messages at the boundary")
	result, found := findResultFor(msgs, "call_1")
	require.True(t, found)
	assert.Equal(t, "file1.txt\nfile2.txt", result.Content, "a real result must pass through byte-identical")
}
