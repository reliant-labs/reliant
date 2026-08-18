// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
)

// questionStatusesInSnapshot returns the status of every question update the
// snapshot carries, keyed by question id.
func questionStatusesInSnapshot(t *testing.T, snapshot *reliantv1.ChatSyncSnapshot) map[string][]string {
	t.Helper()
	byQuestion := map[string][]string{}
	for _, u := range snapshot.OtherUpdates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION {
			continue
		}
		var payload struct {
			QuestionID string `json:"question_id"`
			Status     string `json:"status"`
		}
		require.NoError(t, json.Unmarshal([]byte(u.DataJson), &payload))
		byQuestion[payload.QuestionID] = append(byQuestion[payload.QuestionID], payload.Status)
	}
	return byQuestion
}

func seedQuestionChat(t *testing.T, repo *db.Repo, ctx context.Context) string {
	t.Helper()
	now := time.Now().UTC()
	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "question flash", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
		WorkflowID: &chatID,
	}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID,
		Status: db.Active(), CreatedAt: now,
	}))
	return chatID
}

type questionResolveTemporalClient struct{}

func (*questionResolveTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-1"},
		},
	}, nil
}

func (*questionResolveTemporalClient) TerminateWorkflow(context.Context, string, string, string, ...interface{}) error {
	return nil
}

// TestChatSnapshot_DoesNotReplayAnsweredQuestion is the regression behind
// "when I open a chat that has a previous ask I've already answered, I often
// see it briefly pop up".
//
// The snapshot is the whole of what a client knows when it opens a chat, and
// the frontend drives its pending-question cache from it: a "pending" question
// update sets the cache, "resolved" clears it. A question writes both rows over
// its life under DISTINCT entity_ids (EntityIDForQuestion embeds a timestamp),
// so per-entity dedup used to keep BOTH and the snapshot replayed the opening
// gate alongside its own closure. The client applied them in order, so the
// terminal state was right — but the already-answered question rendered for the
// frames in between. Hence "often", and hence a flash rather than a stuck
// question.
//
// The fix makes the stale row unreachable rather than merely short-lived: the
// snapshot must carry the question's LATEST status and nothing else.
func TestChatSnapshot_DoesNotReplayAnsweredQuestion(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedQuestionChat(t, repo, ctx)
	questionID := uuid.NewString()

	// The gate opens...
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: questionID, ChatID: chatID, Status: "pending",
		Metadata: `{"type":"ask_user","questions":[{"question":"Ship it?"}]}`,
	}))
	time.Sleep(time.Millisecond)
	// ...and the user answers it.
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: questionID, ChatID: chatID, Status: "resolved",
	}))

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	statuses := questionStatusesInSnapshot(t, snapshot)
	require.NotContains(t, statuses[questionID], "pending",
		"an answered question must not be replayed as pending — the client renders it "+
			"until the resolved row lands behind it, which is the flash on open")
	require.Equal(t, []string{"resolved"}, statuses[questionID],
		"the snapshot should carry exactly the question's latest status")
}

// A question that is genuinely still open must survive the dedup. This is the
// half that keeps the fix from becoming "never show questions on open".
func TestChatSnapshot_StillDeliversUnansweredQuestion(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	chatID := seedQuestionChat(t, repo, ctx)

	answered := uuid.NewString()
	stillOpen := uuid.NewString()

	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: answered, ChatID: chatID, Status: "pending",
	}))
	time.Sleep(time.Millisecond)
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: answered, ChatID: chatID, Status: "resolved",
	}))
	time.Sleep(time.Millisecond)
	// A later, unanswered gate — the run is parked on this one right now.
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: stillOpen, ChatID: chatID, Status: "pending",
		Metadata: `{"type":"ask_user","questions":[{"question":"Which branch?"}]}`,
	}))

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	statuses := questionStatusesInSnapshot(t, snapshot)
	require.Equal(t, []string{"pending"}, statuses[stillOpen],
		"a live gate must still reach a client opening the chat, or the run looks stalled with no way to answer")
	require.Equal(t, []string{"resolved"}, statuses[answered])
}

// TestTerminateChat_EmitsQuestionResolved covers the second way an answered
// question comes back: a resolve path that updates the DB row but never tells
// the feed.
//
// TerminateChat voids the pending question so the run stops awaiting input, but
// it used to skip EmitQuestionUpdate. The questions row went to resolved while
// the newest question update on the feed still read "pending" — so the snapshot
// replayed a gate that nothing was waiting on and that the user could not
// answer. Unlike the dedup bug this one does not self-correct, because no
// "resolved" row is ever written to land behind it.
func TestTerminateChat_EmitsQuestionResolved(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	chatID := seedQuestionChat(t, repo, ctx)

	questionID := uuid.NewString()
	require.NoError(t, repo.CreateQuestion(ctx, &db.Question{
		ID: questionID, ChatID: chatID, WorkflowID: "wf-1", ThreadID: chatID,
		StepID: "ask", Status: db.QuestionStatusPending, CreatedAt: now,
	}))
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: questionID, ChatID: chatID, WorkflowID: "wf-1",
		ThreadID: chatID, StepID: "ask", Status: "pending",
		Metadata: `{"type":"ask_user","questions":[{"question":"Ship it?"}]}`,
	}))

	runSvc := runs.NewService(repo, &questionResolveTemporalClient{}, nil)
	require.NoError(t, runSvc.Terminate(ctx, chatID))

	q, err := repo.GetQuestionByID(ctx, questionID)
	require.NoError(t, err)
	require.Equal(t, db.QuestionStatusResolved, q.Status,
		"terminate must void the pending question")

	snapshot, _, err := NewStreamingService(repo, nil, nil, nil).buildChatSnapshot(ctx, chatID)
	require.NoError(t, err)

	statuses := questionStatusesInSnapshot(t, snapshot)
	require.Equal(t, []string{"resolved"}, statuses[questionID],
		"terminating a chat must announce the gate closed; otherwise every later open "+
			"of this chat replays a question that can never be answered")
}
