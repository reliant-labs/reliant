// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A tool call id is chosen by the model provider. #560 fixed the one driver
// that reused an id on purpose, but a provider can still repeat one — a local
// OpenAI-compatible server answering `call_0` in every conversation is enough.
// Reliant keys a call's record, its result and a spawn's report by that id, so
// these tests pin that a repeated id never carries anything across a chat:
// not an answer, not a status, not a report.

// isolationChats is one database holding several chats of one user.
type isolationChats struct {
	h                 *IdempotencyTestHelper
	projectID, userID string
}

func newIsolationChats(t *testing.T) *isolationChats {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	t.Cleanup(h.Cleanup)
	c := &isolationChats{h: h, projectID: uuid.New().String(), userID: uuid.New().String()}
	h.CreateTestProject(context.Background(), c.projectID, c.userID)
	return c
}

// chat creates a chat with its root thread (id == chat id). Not
// CreateTestChat: that also creates a fixed thread "0", so a second chat in
// the same database collides on it.
func (c *isolationChats) chat(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	chatID := uuid.New().String()
	require.NoError(t, c.h.Repo().CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: c.projectID, UserID: c.userID}))
	c.thread(t, chatID, chatID, nil)
	return chatID
}

func (c *isolationChats) thread(t *testing.T, chatID, threadID string, parent *string) {
	t.Helper()
	ctx := context.Background()
	_, err := c.h.Repo().CreateThread(ctx, &db.Thread{ID: threadID, ChatID: chatID, ParentThreadID: parent})
	require.NoError(t, err)
	_, err = c.h.Repo().CreateContextWindow(ctx, &db.ContextWindow{ID: chatID + ":" + threadID + ":0", ThreadID: threadID})
	require.NoError(t, err)
}

// assistantMessage records an assistant message in thread and returns its id:
// the message a tool call's record points at.
func (c *isolationChats) assistantMessage(t *testing.T, chatID, thread string) string {
	t.Helper()
	ctx := context.Background()
	ordinal, err := c.h.Repo().GetNextOrdinal(ctx, thread)
	require.NoError(t, err)
	seq, err := c.h.Repo().GetNextSeq(ctx, chatID, thread)
	require.NoError(t, err)
	id := uuid.New().String()
	now := time.Now().UTC()
	require.NoError(t, c.h.Repo().CreateMessage(ctx, &db.Message{
		ID: id, ChatID: chatID, ThreadID: thread, Role: reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal: ordinal, Seq: seq, ContextWindowID: chatID + ":" + thread + ":0", CreatedAt: now, UpdatedAt: now,
	}))
	return id
}

// runTool dispatches one tool call through the ExecuteTools activity, as the
// agent loop does, and returns the result the model would be shown.
func (c *isolationChats) runTool(t *testing.T, chatID, thread, toolCallID, input string, executor *mockToolExecutor) *ExecuteToolsOutput {
	t.Helper()
	var output ExecuteToolsOutput
	require.NoError(t, c.h.ExecuteActivity(NewExecuteToolsActivity(c.h.Repo(), executor).Execute, ExecuteToolsInput{
		ChatID:    chatID,
		Thread:    thread,
		ToolCalls: []message.ToolCall{{ID: toolCallID, Name: "bash", Input: input}},
	}, &output))
	require.Len(t, output.ToolResults, 1)
	return &output
}

func executorAnswering(toolCallID, content string) *mockToolExecutor {
	executor := newMockToolExecutor()
	executor.SetResult(toolCallID, &toolexec.ToolResult{Success: true, Content: content})
	return executor
}

// Chat A ran `bash` as call "bash" and it finished. Chat B's model now calls
// `bash`, also as "bash". Chat B's call must RUN — it used to be answered with
// chat A's recorded output, because the "already terminal, return the recorded
// result" check matched the id alone — and running it must not overwrite what
// chat A recorded.
func TestRepro_ToolCallWithAReusedIDIsNotAnsweredFromAnotherChat(t *testing.T) {
	c := newIsolationChats(t)
	ctx := context.Background()
	chatA, chatB := c.chat(t), c.chat(t)

	c.runTool(t, chatA, chatA, "bash", `{"command":"cat /a/secret"}`, executorAnswering("bash", "chat A's private output"))

	executorB := executorAnswering("bash", "chat B's own output")
	outB := c.runTool(t, chatB, chatB, "bash", `{"command":"ls /b"}`, executorB)
	require.Equal(t, 1, executorB.GetExecutionCount("bash"),
		"chat B's bash call was not executed; it was answered with: %q", outB.ToolResults[0].GetContent())
	require.Equal(t, "chat B's own output", outB.ToolResults[0].GetContent())

	call, err := c.h.Repo().GetToolCall(ctx, "bash")
	require.NoError(t, err)
	require.Equal(t, chatA, call.ChatID, "chat A's call record must stay chat A's")
	require.JSONEq(t, `{"command":"cat /a/secret"}`, string(call.Input))
	result, err := c.h.Repo().GetToolCallResult(ctx, "bash")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "chat A's private output", result.Content, "chat B's output must not replace chat A's recorded result")
}

// The same id still running in chat A. Nothing short-circuits here — the row
// is not terminal — so chat B always ran, and its status and result writes
// then landed on chat A's record: A's running call read as finished, with B's
// input and B's output.
func TestRepro_ToolCallWithAReusedIDDoesNotOverwriteAnotherChatsRunningCall(t *testing.T) {
	c := newIsolationChats(t)
	ctx := context.Background()
	chatA, chatB := c.chat(t), c.chat(t)

	now := time.Now().UTC()
	require.NoError(t, c.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID: "shell-1", ChatID: chatA, ThreadID: &chatA, ToolName: "bash",
		Input: []byte(`{"command":"sleep 600"}`), Status: core.ToolCallStatusExecuting,
		RequestedAt: now, StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))

	outB := c.runTool(t, chatB, chatB, "shell-1", `{"command":"ls /b"}`, executorAnswering("shell-1", "chat B's own output"))
	require.Equal(t, "chat B's own output", outB.ToolResults[0].GetContent())

	call, err := c.h.Repo().GetToolCall(ctx, "shell-1")
	require.NoError(t, err)
	require.Equal(t, chatA, call.ChatID)
	require.Equal(t, core.ToolCallStatusExecuting, call.Status, "chat A's call is still running; chat B finishing must not end it")
	require.JSONEq(t, `{"command":"sleep 600"}`, string(call.Input))
	result, err := c.h.Repo().GetToolCallResult(ctx, "shell-1")
	require.NoError(t, err)
	require.Nil(t, result, "chat B's output must not be recorded as the result of chat A's call")
}

// The prior-result check is scoped to the thread as well as the chat: a spawned
// thread reusing its parent's id is a different call, not a re-dispatch.
func TestToolCallIDIsolation_PriorResultFromAnotherThreadIsNotReused(t *testing.T) {
	c := newIsolationChats(t)
	chatA := c.chat(t)
	child := uuid.New().String()
	c.thread(t, chatA, child, &chatA)

	c.runTool(t, chatA, chatA, "call_0", `{"command":"cat parent.txt"}`, executorAnswering("call_0", "the parent's output"))

	executorChild := executorAnswering("call_0", "the child's own output")
	out := c.runTool(t, chatA, child, "call_0", `{"command":"cat child.txt"}`, executorChild)
	require.Equal(t, 1, executorChild.GetExecutionCount("call_0"),
		"the child thread's call was not executed; it was answered with: %q", out.ToolResults[0].GetContent())
	require.Equal(t, "the child's own output", out.ToolResults[0].GetContent())
}

// A re-dispatch in the SAME chat and thread is what the check exists for, and
// must still be answered from the record rather than run twice.
func TestToolCallIDIsolation_RedispatchInTheSameThreadIsStillNotReExecuted(t *testing.T) {
	c := newIsolationChats(t)
	chatA := c.chat(t)

	executor := executorAnswering("call_0", "ran once")
	c.runTool(t, chatA, chatA, "call_0", `{"command":"date"}`, executor)
	out := c.runTool(t, chatA, chatA, "call_0", `{"command":"date"}`, executor)
	require.Equal(t, 1, executor.GetExecutionCount("call_0"))
	require.Equal(t, "ran once", out.ToolResults[0].GetContent())
}

// spawnReportFor reports a background spawn's outcome the way its detached
// goroutine does.
func (c *isolationChats) spawnReportFor(t *testing.T, chatID, child, parent, toolCallID, body string) error {
	t.Helper()
	var out EnqueueAgentMessageOutput
	return c.h.ExecuteActivity(NewEnqueueAgentMessageActivity(c.h.Repo()).Execute, EnqueueAgentMessageInput{
		ChatID: chatID, FromThreadID: child, ToThreadID: parent,
		Kind: int32(core.AgentMessageKindCompletion), Body: body, ToolCallID: toolCallID,
	}, &out)
}

// Chat A's spawn and chat B's spawn both carry tool_call_id "spawn" (what the
// Vertex Gemini driver emitted before #560). B's child reports; B's parent
// must receive it. The report slot was keyed by tool_call_id alone, so B's
// report found A's in it and was dropped as "already reported" with no error.
func TestRepro_SpawnReportWithAReusedToolCallIDReachesItsParent(t *testing.T) {
	c := newIsolationChats(t)
	ctx := context.Background()
	chatA, chatB := c.chat(t), c.chat(t)
	childA, childB := uuid.New().String(), uuid.New().String()
	c.thread(t, chatA, childA, &chatA)
	c.thread(t, chatB, childB, &chatB)

	require.NoError(t, c.spawnReportFor(t, chatA, childA, chatA, "spawn", "chat A's child: result A"))
	require.NoError(t, c.spawnReportFor(t, chatB, childB, chatB, "spawn", "chat B's child: result B"))

	queuedB, err := c.h.Repo().ListQueuedAgentMessagesForThread(ctx, chatB)
	require.NoError(t, err)
	require.Len(t, queuedB, 1, "chat B's parent has no report in its mailbox: chat B's child's result was dropped")
	require.Equal(t, "chat B's child: result B", queuedB[0].Body)

	queuedA, err := c.h.Repo().ListQueuedAgentMessagesForThread(ctx, chatA)
	require.NoError(t, err)
	require.Len(t, queuedA, 1)
	require.Equal(t, "chat A's child: result A", queuedA[0].Body, "chat B's report must not touch chat A's")
}

// Within one chat, two different spawns reporting under one id cannot both
// have the slot. The second must fail — the activity's error is what makes
// the loss visible — rather than be swallowed as an idempotent retry. A real
// retry (the same child reporting again) stays a no-op.
func TestToolCallIDIsolation_SecondSpawnReusingAnIDInOneChatFailsLoudly(t *testing.T) {
	c := newIsolationChats(t)
	ctx := context.Background()
	chatA := c.chat(t)
	first, second := uuid.New().String(), uuid.New().String()
	c.thread(t, chatA, first, &chatA)
	c.thread(t, chatA, second, &chatA)

	require.NoError(t, c.spawnReportFor(t, chatA, first, chatA, "call_0", "first spawn's result"))
	require.NoError(t, c.spawnReportFor(t, chatA, first, chatA, "call_0", "first spawn's result"),
		"the same child reporting again is a retry and must stay idempotent")

	err := c.spawnReportFor(t, chatA, second, chatA, "call_0", "second spawn's result")
	require.Error(t, err, "a different spawn's report must not be silently discarded")
	require.ErrorContains(t, err, core.ErrSpawnReportSlotTaken.Error())

	queued, err := c.h.Repo().ListQueuedAgentMessagesForThread(ctx, chatA)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	require.Equal(t, "first spawn's result", queued[0].Body)
}

// History recovery puts a recorded result back in front of the model when the
// tool message that should have carried it is missing. It must only use a
// result recorded for THIS call — the one the assistant message carries — or
// it hands the model another chat's output as if this call had produced it.
func TestToolCallIDIsolation_HistoryRecoveryUsesOnlyThisCallsResult(t *testing.T) {
	c := newIsolationChats(t)
	ctx := context.Background()
	chatA, chatB := c.chat(t), c.chat(t)

	now := time.Now().UTC()
	msgA := c.assistantMessage(t, chatA, chatA)
	require.NoError(t, c.h.Repo().UpsertToolCall(ctx, &core.ToolCall{
		ID: "call_0", ChatID: chatA, ThreadID: &chatA, MessageID: &msgA, ToolName: "bash",
		Status: core.ToolCallStatusCompleted, RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, c.h.Repo().UpsertToolCallResult(ctx, chatA, &core.ToolCallResult{
		ToolCallID: "call_0", Content: "chat A's private output", CreatedAt: now, UpdatedAt: now,
	}))
	historyB := []message.Message{userWithText("u-b", "go"), assistantWithToolCall(c.assistantMessage(t, chatB, chatB), "call_0", "bash")}
	out := recoverPersistedToolResults(ctx, c.h.Repo(), historyB)
	require.Len(t, out, len(historyB), "chat B's history must not be given chat A's result: %+v", out)

	historyA := []message.Message{userWithText("u-a", "go"), assistantWithToolCall(msgA, "call_0", "bash")}
	out = recoverPersistedToolResults(ctx, c.h.Repo(), historyA)
	require.Len(t, out, len(historyA)+1, "chat A's own result must still be recovered")
	require.Equal(t, "chat A's private output", out[len(out)-1].ToolResults()[0].Content)
}
