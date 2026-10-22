// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A thread parked on a pending blocking question keeps its mailbox even if it
// fails: its question is what resumes it (FAILED keeps questions answerable),
// and the call_llm after the answer drains the mailbox. That is where the
// answer to an async question waits while its thread is parked on a blocking
// one (question/async_answer.go). Once no blocking question is pending, the
// mailbox of a terminal thread is swept as before.
func TestMailboxOfAThreadParkedOnABlockingQuestionIsNotSwept(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-parked-mailbox"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflowWithParent(t, repo, "wf-pm-root", chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, "th-pm-root", chatID, "wf-pm-root", ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-pm-spawn", chatID, "wf-pm-root", ThreadStatusRunning)
	insertTestQuestion(t, repo, "q-blocking", chatID, "wf-pm-root", "th-pm-spawn")
	require.NoError(t, repo.EnqueueAgentMessage(ctx, &AgentMessage{
		ID: "am-answer", FromThreadID: "th-pm-root", ChatID: chatID, ToThreadID: "th-pm-spawn",
		Kind: core.AgentMessageKindHumanMessage, Body: "Answer to the question you asked without waiting",
		Status: core.AgentMessageStatusQueued, CreatedAt: time.Now(),
	}))

	now := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, "th-pm-spawn", ThreadStatusFailed, &now)
	require.NoError(t, err)
	requireQuestionStatus(t, repo, "q-blocking", QuestionStatusPending)

	orphaned, err := repo.ListThreadsWithOrphanedAgentMessages(ctx)
	require.NoError(t, err)
	assert.NotContains(t, orphaned, "th-pm-spawn", "a failed thread parked on a question is not orphaned")
	moved, err := repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, "th-pm-spawn")
	require.NoError(t, err)
	assert.Zero(t, moved)
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, "th-pm-spawn")
	require.NoError(t, err)
	require.Len(t, queued, 1, "the answer still waits for the thread's next call_llm")

	// The blocking question gone (answered, or closed with the thread), the
	// mailbox of the terminal thread is swept as it always was.
	require.NoError(t, repo.ResolveQuestion(ctx, "q-blocking", nil))
	orphaned, err = repo.ListThreadsWithOrphanedAgentMessages(ctx)
	require.NoError(t, err)
	assert.Contains(t, orphaned, "th-pm-spawn")
	moved, err = repo.MarkQueuedAgentMessagesUndeliveredForThread(ctx, "th-pm-spawn")
	require.NoError(t, err)
	assert.Equal(t, int64(1), moved)
}
