// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A thread that finished has no pending question (question_close.go). The
// run-end cleanup used to deny the chat's pending approvals and leave its
// questions alone, so a question asked by a spawn that then finished, failed
// or was cancelled stayed "pending" forever: the chat read as awaiting input
// and the composer kept offering a gate nothing was behind.

func insertTestQuestion(t *testing.T, repo *Repo, id, chatID, workflowID, threadID string) {
	t.Helper()
	require.NoError(t, repo.CreateQuestion(context.Background(), &Question{
		ID: id, ChatID: chatID, WorkflowID: workflowID, ThreadID: threadID,
		StepID: "execute_tools", Status: QuestionStatusPending, CreatedAt: time.Now().UTC(),
	}))
}

func requireQuestionStatus(t *testing.T, repo *Repo, id string, want int) *Question {
	t.Helper()
	q, err := repo.GetQuestionByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, want, q.Status, "question %s", id)
	return q
}

// resolvedQuestionUpdates returns the ids of the questions the chat's feed
// last describes as resolved — what a client opening the chat receives.
func resolvedQuestionUpdates(t *testing.T, repo *Repo, chatID string) []string {
	t.Helper()
	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(context.Background(), chatID)
	require.NoError(t, err)
	var ids []string
	for _, u := range updates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION {
			continue
		}
		var payload struct {
			QuestionID string `json:"question_id"`
			Status     string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(u.Data, &payload))
		if payload.Status == "resolved" {
			ids = append(ids, payload.QuestionID)
		}
	}
	return ids
}

func TestUpdateThreadStatus_FinishingAThreadClosesItsQuestions(t *testing.T) {
	t.Parallel()

	for _, status := range []ThreadStatus{ThreadStatusCompleted, ThreadStatusCancelled} {
		t.Run(core.ThreadStatusLabel(status), func(t *testing.T) {
			repo, cleanup := SetupTestDB(t)
			defer cleanup()
			ctx := context.Background()

			chatID := "chat-qc-thread"
			createActivityTestChat(t, repo, chatID)
			insertTestWorkflowWithParent(t, repo, "wf-qc-root", chatID, nil, Active())
			insertTestThreadForWorkflow(t, repo, "th-qc-root", chatID, "wf-qc-root", ThreadStatusRunning)
			insertTestThreadForWorkflow(t, repo, "th-qc-spawn", chatID, "wf-qc-root", ThreadStatusRunning)

			insertTestQuestion(t, repo, "q-spawn-old", chatID, "wf-qc-root", "th-qc-spawn")
			insertTestQuestion(t, repo, "q-spawn-new", chatID, "wf-qc-root", "th-qc-spawn")
			insertTestQuestion(t, repo, "q-root", chatID, "wf-qc-root", "th-qc-root")
			insertTestQuestion(t, repo, "q-spawn-answered", chatID, "wf-qc-root", "th-qc-spawn")
			answer := `{"answers":[{"selected":["Bold"]}]}`
			require.NoError(t, repo.ResolveQuestion(ctx, "q-spawn-answered", &answer))

			now := time.Now().UTC()
			_, err := repo.UpdateThreadStatus(ctx, "th-qc-spawn", status, &now)
			require.NoError(t, err)

			requireQuestionStatus(t, repo, "q-spawn-old", QuestionStatusResolved)
			requireQuestionStatus(t, repo, "q-spawn-new", QuestionStatusResolved)
			requireQuestionStatus(t, repo, "q-root", QuestionStatusPending)
			answered := requireQuestionStatus(t, repo, "q-spawn-answered", QuestionStatusResolved)
			require.NotNil(t, answered.ResponseData)
			assert.Equal(t, answer, *answered.ResponseData, "an answer that got there first is kept")

			assert.ElementsMatch(t, []string{"q-spawn-old", "q-spawn-new"}, resolvedQuestionUpdates(t, repo, chatID),
				"each question this closed is announced as resolved on the chat's feed, and only those")
		})
	}
}

// A failed thread is not finished: its run is resumable, and the resume puts
// the question's wait back (reset-and-replay, SendMessage's answer-as-resume,
// a stopped sub-agent resumed by a message), which only an answer to the
// still-pending row can end.
func TestUpdateThreadStatus_FailingAThreadKeepsItsQuestionsAnswerable(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-qc-failed"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflowWithParent(t, repo, "wf-qc-failed", chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, "th-qc-failed", chatID, "wf-qc-failed", ThreadStatusRunning)
	insertTestQuestion(t, repo, "q-failed", chatID, "wf-qc-failed", "th-qc-failed")

	now := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, "th-qc-failed", ThreadStatusFailed, &now)
	require.NoError(t, err)
	requireQuestionStatus(t, repo, "q-failed", QuestionStatusPending)

	require.NoError(t, repo.CascadeTerminalStatusToThreadSubtree(ctx, "wf-qc-failed", StopReasonFailed))
	requireQuestionStatus(t, repo, "q-failed", QuestionStatusPending)
}

// A live status write (started) is not an end.
func TestUpdateThreadStatus_StartingAThreadLeavesItsQuestions(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-qc-live"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflowWithParent(t, repo, "wf-qc-live", chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, "th-qc-live", chatID, "wf-qc-live", ThreadStatusRunning)
	insertTestQuestion(t, repo, "q-live", chatID, "wf-qc-live", "th-qc-live")

	_, err := repo.UpdateThreadStatus(ctx, "th-qc-live", ThreadStatusRunning, nil)
	require.NoError(t, err)

	requireQuestionStatus(t, repo, "q-live", QuestionStatusPending)
}

func TestCascadeTerminalStatusToThreadSubtree_ClosesTheSubtreesQuestions(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-qc-cascade"
	createActivityTestChat(t, repo, chatID)
	rootID, childID, grandchildID := "wf-qcc-root", "wf-qcc-child", "wf-qcc-grandchild"
	insertTestWorkflowWithParent(t, repo, rootID, chatID, nil, Active())
	insertTestWorkflowWithParent(t, repo, childID, chatID, &rootID, Active())
	insertTestWorkflowWithParent(t, repo, grandchildID, chatID, &childID, Active())
	insertTestThreadForWorkflow(t, repo, "th-qcc-root", chatID, rootID, ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-qcc-child", chatID, childID, ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-qcc-grandchild", chatID, grandchildID, ThreadStatusRunning)

	insertTestQuestion(t, repo, "q-root", chatID, rootID, "th-qcc-root")
	insertTestQuestion(t, repo, "q-child", chatID, childID, "th-qcc-child")
	// Recorded against the root's workflow id, asked on the grandchild's
	// thread: matched by its thread.
	insertTestQuestion(t, repo, "q-grandchild", chatID, rootID, "th-qcc-grandchild")

	// The child's subtree ends; its parent keeps running.
	require.NoError(t, repo.CascadeTerminalStatusToThreadSubtree(ctx, childID, StopReasonCancelled))

	requireQuestionStatus(t, repo, "q-child", QuestionStatusResolved)
	requireQuestionStatus(t, repo, "q-grandchild", QuestionStatusResolved)
	requireQuestionStatus(t, repo, "q-root", QuestionStatusPending)

	// A pause is not an end.
	require.NoError(t, repo.CascadeTerminalStatusToThreadSubtree(ctx, rootID, StopReasonPaused))
	requireQuestionStatus(t, repo, "q-root", QuestionStatusPending)

	require.NoError(t, repo.CascadeTerminalStatusToThreadSubtree(ctx, rootID, StopReasonCompleted))
	requireQuestionStatus(t, repo, "q-root", QuestionStatusResolved)
}

func TestReapOrphanedThreads_ClosesTheReapedThreadsQuestions(t *testing.T) {
	t.Parallel()

	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-qc-reap"
	createActivityTestChat(t, repo, chatID)
	insertTestWorkflowWithParent(t, repo, "wf-qcr-dead", chatID, nil, Cancelled())
	insertTestWorkflowWithParent(t, repo, "wf-qcr-failed", chatID, nil, Failed())
	insertTestWorkflowWithParent(t, repo, "wf-qcr-live", chatID, nil, Active())
	insertTestThreadForWorkflow(t, repo, "th-qcr-dead", chatID, "wf-qcr-dead", ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-qcr-live", chatID, "wf-qcr-live", ThreadStatusRunning)
	insertTestThreadForWorkflow(t, repo, "th-qcr-failed", chatID, "wf-qcr-failed", ThreadStatusRunning)
	insertTestQuestion(t, repo, "q-failed", chatID, "wf-qcr-failed", "th-qcr-failed")
	insertTestQuestion(t, repo, "q-dead", chatID, "wf-qcr-dead", "th-qcr-dead")
	insertTestQuestion(t, repo, "q-live", chatID, "wf-qcr-live", "th-qcr-live")

	reaped, err := repo.ReapOrphanedThreads(ctx)
	require.NoError(t, err)
	require.Len(t, reaped, 2)

	requireQuestionStatus(t, repo, "q-dead", QuestionStatusResolved)
	requireQuestionStatus(t, repo, "q-live", QuestionStatusPending)
	requireQuestionStatus(t, repo, "q-failed", QuestionStatusPending)
}
