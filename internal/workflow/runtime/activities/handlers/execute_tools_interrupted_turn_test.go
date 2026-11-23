// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// A batch the user interrupted ("Interrupt & send now") is dispatched once
// more with RuntimeContext.InterruptedTurn, after the interrupt cancelled the
// dispatch running it (runtime/interrupt_settle.go). That dispatch must run
// NOTHING and still answer every call, so the turn's tool_calls all get a
// tool_result and the loop reaches the call_llm that delivers the user's
// message. Each call's answer comes from its durable row.
func TestInterruptedTurn_AnswersEveryCallWithoutRunningAny(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()
	thread := f.chatID
	now := time.Now().UTC()

	id := func(label string) string { return "toolu_" + label + "_" + uuid.NewString() }
	finished, running, background, neverStarted, response :=
		id("finished"), id("running"), id("background"), id("never"), id("response")

	upsert := func(callID, name string, status core.ToolCallStatus) {
		t.Helper()
		call := &db.ToolCall{
			ID: callID, ChatID: f.chatID, ThreadID: &thread, ToolName: name, Status: status,
			RequestedAt: now, CreatedAt: now, UpdatedAt: now,
		}
		if status.IsTerminal() {
			call.CompletedAt = &now
		}
		require.NoError(t, f.h.Repo().UpsertToolCall(ctx, call))
	}
	// Finished before the interrupt (or recorded Cancelled by InterruptThread).
	upsert(finished, "shell", core.ToolCallStatusCompleted)
	require.NoError(t, f.h.Repo().UpsertToolCallResult(ctx, f.chatID, &db.ToolCallResult{
		ToolCallID: finished, Content: "real output", CreatedAt: now, UpdatedAt: now,
	}))
	// Started, no outcome recorded.
	upsert(running, "shell", core.ToolCallStatusExecuting)
	// Handed off to a background process that is still running.
	upsert(background, "shell", core.ToolCallStatusBackgrounded)
	require.NoError(t, f.h.Repo().UpsertToolCallResult(ctx, f.chatID, &db.ToolCallResult{
		ToolCallID: background, Content: "started in the background", CreatedAt: now, UpdatedAt: now,
	}))
	// neverStarted has no row: it was still in its activation preview.

	executor := newMockToolExecutor()
	for _, callID := range []string{finished, running, background, neverStarted, response} {
		executor.SetResult(callID, &toolexec.ToolResult{Success: true, Content: "should never run"})
	}

	input := ExecuteToolsInput{
		ChatID: f.chatID,
		Thread: thread,
		ToolCalls: []message.ToolCall{
			{ID: finished, Name: "shell", Input: `{"command":"ls"}`},
			{ID: running, Name: "shell", Input: `{"command":"sleep 600"}`},
			{ID: background, Name: "shell", Input: `{"command":"npm run dev"}`},
			{ID: neverStarted, Name: "update_trigger_activation", Input: `{"trigger_id":"trg-1"}`},
			{ID: response, Name: "submit_response", Input: `{"value":"done"}`},
		},
		ExpectedResponseTools: []string{"submit_response"},
	}.V3()
	input.Runtime.InterruptedTurn = true

	var output ExecuteToolsOutput
	require.NoError(t, f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), workspaceRowsOf(f.h.Repo()), toolCallsOf(f.h.Repo()), executor).Execute, input, &output))

	for _, callID := range []string{finished, running, background, neverStarted, response} {
		assert.Zero(t, executor.GetExecutionCount(callID), "an interrupted turn runs nothing: %s", callID)
	}

	require.Len(t, output.ToolResults, 5, "every call of the turn gets a tool_result")
	byID := map[string]*ToolResultMsgView{}
	for _, r := range output.ToolResults {
		byID[r.GetToolCallId()] = &ToolResultMsgView{Content: r.GetContent(), IsError: r.GetIsError()}
	}
	assert.Equal(t, "real output", byID[finished].Content, "a finished call keeps its recorded result")
	assert.False(t, byID[finished].IsError)
	assert.Equal(t, CancelledByInterruptToolResultContent, byID[running].Content,
		"a started call's effects are unknown, and its result says so")
	assert.Equal(t, "started in the background", byID[background].Content,
		"a backgrounded call keeps the result it already reported")
	assert.Equal(t, NotRunInterruptedToolResultContent, byID[neverStarted].Content,
		"a call that never started is answered as not run")
	assert.Equal(t, NotRunInterruptedToolResultContent, byID[response].Content,
		"a response tool is not answered as the agent's final word: the loop must reach the call_llm that reads the user's message")
	assert.Empty(t, output.GetResponseData().GetFields()["submit_response"].GetStructValue().GetFields(),
		"no response data, so the loop does not end on it")

	status := func(callID string) core.ToolCallStatus {
		t.Helper()
		call, err := f.h.Repo().GetToolCall(ctx, callID)
		require.NoError(t, err)
		require.NotNil(t, call, "a row for %s", callID)
		return call.Status
	}
	assert.Equal(t, core.ToolCallStatusCompleted, status(finished), "a finished call's row is untouched")
	assert.Equal(t, core.ToolCallStatusCancelled, status(running))
	assert.Equal(t, core.ToolCallStatusBackgrounded, status(background),
		"a backgrounded row is the reconciler's to close from the process's real outcome")
	assert.Equal(t, core.ToolCallStatusCancelled, status(neverStarted),
		"a call that never started is recorded, so the UI shows it stopped rather than nothing")
	recorded, err := store.FindToolCallResult(ctx, f.h.Repo().ORM(), neverStarted)
	require.NoError(t, err)
	require.NotNil(t, recorded)
	assert.Equal(t, NotRunInterruptedToolResultContent, recorded.Content,
		"the durable result is the one the model was shown")
}

// ToolResultMsgView is the part of a tool result these assertions read.
type ToolResultMsgView struct {
	Content string
	IsError bool
}

// unreadableToolCallStore is a store whose tool-call row lookups fail, as a
// database that is briefly unavailable fails them.
type unreadableToolCallStore struct{ ToolCalls }

func (unreadableToolCallStore) FindToolCall(context.Context, string) (*core.ToolCall, error) {
	return nil, errors.New("connection refused")
}

// A row the settle cannot read proves neither that the call ran nor that it
// did not: it is answered as of unknown effect, never as "did nothing", and
// nothing is written over the row it could not see.
func TestInterruptedTurn_AnUnreadableRowIsOfUnknownEffect(t *testing.T) {
	t.Parallel()

	f := setupDurableStatusFixture(t)
	defer f.h.Cleanup()
	ctx := context.Background()
	thread := f.chatID
	now := time.Now().UTC()
	running := "toolu_running_" + uuid.NewString()
	require.NoError(t, f.h.Repo().UpsertToolCall(ctx, &db.ToolCall{
		ID: running, ChatID: f.chatID, ThreadID: &thread, ToolName: "shell", Status: core.ToolCallStatusExecuting,
		RequestedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	executor := newMockToolExecutor()
	executor.SetResult(running, &toolexec.ToolResult{Success: true, Content: "should never run"})
	input := ExecuteToolsInput{
		ChatID:    f.chatID,
		Thread:    thread,
		ToolCalls: []message.ToolCall{{ID: running, Name: "shell", Input: `{"command":"sleep 600"}`}},
	}.V3()
	input.Runtime.InterruptedTurn = true

	var output ExecuteToolsOutput
	store := unreadableToolCallStore{ToolCalls: toolCallsOf(f.h.Repo())}
	require.NoError(t, f.h.ExecuteActivity(NewExecuteToolsActivity(f.h.Repo(), workspaceRowsOf(f.h.Repo()), store, executor).Execute, input, &output))

	assert.Zero(t, executor.GetExecutionCount(running), "an interrupted turn runs nothing")
	require.Len(t, output.ToolResults, 1)
	assert.Equal(t, CancelledByInterruptToolResultContent, output.ToolResults[0].GetContent(),
		"a call whose row could not be read may have started: its effects are unknown")
	assert.True(t, output.ToolResults[0].GetIsError())
	call, err := f.h.Repo().GetToolCall(ctx, running)
	require.NoError(t, err)
	assert.Equal(t, core.ToolCallStatusExecuting, call.Status, "nothing is written over a row the settle could not read")
}
