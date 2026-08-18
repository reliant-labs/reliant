// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A question's 24h timeout resolves the row through QuestionResolveActivity.
// That closes the gate as far as the run is concerned — but clients drive their
// pending-question state from the chat update feed, and this path used to write
// nothing to it. The newest question update stayed "pending" forever, so every
// later open of the chat replayed a gate that had already expired: the user
// sees a question reappear that nothing is waiting on and that answering does
// not help.
//
// The resolve must announce itself.
func TestQuestionResolveActivity_EmitsResolvedUpdate(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC()

	chatID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "timed-out gate", ProjectID: "test-project", UserID: "test-user",
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	questionID := uuid.NewString()
	require.NoError(t, repo.CreateQuestion(ctx, &db.Question{
		ID: questionID, ChatID: chatID, WorkflowID: "wf-1", ThreadID: chatID,
		StepID: "ask", Status: db.QuestionStatusPending, CreatedAt: now,
	}))
	require.NoError(t, repo.EmitQuestionUpdate(ctx, chatID, db.QuestionUpdate{
		QuestionID: questionID, ChatID: chatID, WorkflowID: "wf-1",
		ThreadID: chatID, StepID: "ask", Status: "pending",
	}))

	// The activity reads activity.GetLogger, so it needs a real activity
	// context — the test environment supplies one.
	resolveActivity := NewQuestionResolveActivity(repo)
	suite := &testsuite.WorkflowTestSuite{}
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(resolveActivity.Execute)

	_, err := env.ExecuteActivity(resolveActivity.Execute, QuestionResolveInput{
		QuestionID: questionID,
	})
	require.NoError(t, err)

	q, err := repo.GetQuestionByID(ctx, questionID)
	require.NoError(t, err)
	require.Equal(t, db.QuestionStatusResolved, q.Status)

	// The feed must now describe the gate as closed. Reading the deduped view
	// is the point: it is exactly what a client opening this chat receives.
	updates, err := repo.GetLatestNonMessageUpdatesPerEntity(ctx, chatID)
	require.NoError(t, err)

	var statuses []string
	for _, u := range updates {
		if u.UpdateType != reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION {
			continue
		}
		var payload struct {
			QuestionID string `json:"question_id"`
			Status     string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(u.Data, &payload))
		if payload.QuestionID == questionID {
			statuses = append(statuses, payload.Status)
		}
	}

	require.Equal(t, []string{"resolved"}, statuses,
		"a timed-out question must be announced as resolved, or the expired gate "+
			"replays to every client that opens this chat afterwards")
}
