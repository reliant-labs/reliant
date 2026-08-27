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
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/threads"
)

// FetchThreadResult builds the tool result a parent reads when its spawn ends.
// Getting "is this a real report?" wrong is invisible to the parent, which is
// why these cases are pinned: on chat 7da3935c-97ec-4843-af78-c3807fe336cb a
// sub-agent was killed mid-edit and reported as a clean success whose entire
// body was "Agent completed but produced no text response".

type fetchResultFixture struct {
	h        *IdempotencyTestHelper
	chatID   string
	threadID string
}

func setupFetchResultFixture(t *testing.T) *fetchResultFixture {
	t.Helper()
	h := NewIdempotencyTestHelper(t)
	ctx := context.Background()

	userID := uuid.New().String()
	projectID := uuid.New().String()
	chatID := uuid.New().String()

	h.CreateTestProject(ctx, projectID, userID)
	h.CreateTestChat(ctx, chatID, projectID, userID)

	return &fetchResultFixture{h: h, chatID: chatID, threadID: chatID}
}

// saveAssistant writes an assistant message. A nil text means the message
// carried no text block at all — the tool-call-only shape a working agent
// leaves behind on every turn that ends in a tool call.
func (f *fetchResultFixture) saveAssistant(t *testing.T, text *string, toolName string) {
	t.Helper()
	ctx := context.Background()

	contextWindowID := f.chatID + ":" + f.threadID + ":0"
	msgID := uuid.New().String()
	ordinal, err := f.h.Repo().GetNextOrdinal(ctx, f.threadID)
	require.NoError(t, err)
	seq, err := f.h.Repo().GetNextSeq(ctx, f.chatID, f.threadID)
	require.NoError(t, err)

	require.NoError(t, f.h.Repo().CreateMessage(ctx, &db.Message{
		ID:              msgID,
		ChatID:          f.chatID,
		Role:            reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT,
		Ordinal:         ordinal,
		Seq:             seq,
		ThreadID:        f.threadID,
		ContextWindowID: contextWindowID,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}))

	position := 0
	if text != nil {
		require.NoError(t, f.h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: msgID,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT,
			Position:  position,
			Content:   text,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}))
		position++
	}
	if toolName != "" {
		// A tool_use block with NULL content — exactly what an `edit` call
		// writes, and what joinTextBlocks correctly contributes nothing for.
		require.NoError(t, f.h.Repo().CreateContentBlock(ctx, &db.MessageContentBlock{
			ID:        uuid.New().String(),
			MessageID: msgID,
			BlockType: reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL,
			Position:  position,
			ToolName:  ptr.Of(toolName),
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}))
	}
}

func (f *fetchResultFixture) fetch(t *testing.T) FetchThreadResultOutput {
	t.Helper()
	activityInstance := NewFetchThreadResultActivity(threads.NewService(f.h.Repo()))

	var output FetchThreadResultOutput
	require.NoError(t, f.h.ExecuteActivity(activityInstance.Execute, FetchThreadResultInput{
		ChatID: f.chatID,
		Thread: f.threadID,
	}, &output))
	return output
}

// THE REGRESSION. An agent whose last act was a tool call was still working
// when it stopped. Reporting that as a success told the parent the child had
// finished and merely had nothing to say, so the parent moved on from work
// that was never completed.
func TestFetchThreadResult_ToolCallOnlyTailIsAnError(t *testing.T) {
	f := setupFetchResultFixture(t)
	defer f.h.Cleanup()

	f.saveAssistant(t, ptr.Of("Now remove the scope.host gates:"), "edit")
	f.saveAssistant(t, nil, "edit") // cut short here — no closing text

	output := f.fetch(t)

	assert.True(t, output.IsError,
		"a thread whose last assistant message carried no text has not reported a result; "+
			"calling that a success is how a spawn killed mid-edit read as complete")
	assert.NotContains(t, output.Content, "produced no text response",
		"the old placeholder described the symptom as if it were a normal outcome")
	assert.Contains(t, output.Content, "still working",
		"the parent needs to know the run was cut short, not that the agent chose silence")
}

// The healthy path must stay untouched: a real closing report is returned
// verbatim, as a success.
func TestFetchThreadResult_ClosingTextIsReturnedVerbatim(t *testing.T) {
	f := setupFetchResultFixture(t)
	defer f.h.Cleanup()

	f.saveAssistant(t, ptr.Of("Intermediate step"), "bash")
	f.saveAssistant(t, ptr.Of("## Done\n\nRemoved the flag and updated the tests."), "")

	output := f.fetch(t)

	assert.False(t, output.IsError)
	assert.Equal(t, "## Done\n\nRemoved the flag and updated the tests.", output.Content)
}

// An agent that produced text AND a trailing tool call in its final message
// has still said something, so it reports normally. This is the boundary that
// keeps the new error from swallowing legitimate reports.
func TestFetchThreadResult_TextWithTrailingToolCallIsNotAnError(t *testing.T) {
	f := setupFetchResultFixture(t)
	defer f.h.Cleanup()

	f.saveAssistant(t, ptr.Of("Here is what I found, running one last check:"), "bash")

	output := f.fetch(t)

	assert.False(t, output.IsError)
	assert.Contains(t, output.Content, "Here is what I found")
}

// A thread with no assistant message at all was already an error; that must
// not regress, and it must stay distinguishable from the case above.
func TestFetchThreadResult_NoAssistantMessageIsAnError(t *testing.T) {
	f := setupFetchResultFixture(t)
	defer f.h.Cleanup()

	f.h.CreateTestUserMessage(context.Background(), f.chatID, f.threadID)

	output := f.fetch(t)

	assert.True(t, output.IsError)
	assert.Contains(t, output.Content, "No assistant response found")
}
