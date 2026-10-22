// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
)

// A run that died parked on an unanswered question is resumed by delivering
// the user's message as that question's answer — but only the question of the
// thread the message is for. In a chat with several agents each can be parked
// on its own question; the chat's newest one, from any thread, used to be
// picked, so a message to the main thread answered a sub-agent's question.

// questionSignalPause records which signal a resume delivered, and whether
// the plain reset-and-replay was taken instead.
type questionSignalPause struct {
	succeedingPauseController
	signals          []string
	interruptResumes int
}

func (p *questionSignalPause) SignalWithRecovery(_ context.Context, _, signalName string, _ interface{}, _ map[string]interface{}) error {
	p.signals = append(p.signals, signalName)
	return nil
}

func (p *questionSignalPause) ResumeInterruptedWorkflow(ctx context.Context, workflowID, chatID string, update map[string]interface{}) (string, error) {
	p.interruptResumes++
	return p.succeedingPauseController.ResumeInterruptedWorkflow(ctx, workflowID, chatID, update)
}

func setupFailedRunWithQuestion(t *testing.T, threadOf func(fx absorbFixture) string) (*db.Repo, context.Context, absorbFixture, *Service, *questionSignalPause, string) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	questionID := uuid.NewString()
	require.NoError(t, repo.CreateQuestion(ctx, &db.Question{
		ID: questionID, ChatID: fx.chatID, WorkflowID: fx.rootThreadID, TemporalWorkflowID: fx.rootThreadID,
		ThreadID: threadOf(fx), StepID: "execute_tools", Status: db.QuestionStatusPending, CreatedAt: time.Now().UTC(),
	}))

	temporal := &wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_FAILED},
	}
	pause := &questionSignalPause{}
	service := &Service{
		deps: Deps{Repo: repo, Temporal: temporal},
		runs: runs.NewService(repo, temporal, pause),
	}
	return repo, ctx, fx, service, pause, questionID
}

func TestSendMessage_FailedRunAnswersTheTargetThreadsQuestion(t *testing.T) {
	repo, ctx, fx, service, pause, questionID := setupFailedRunWithQuestion(t, func(fx absorbFixture) string { return fx.rootThreadID })

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "go with the minimal one"))
	require.NoError(t, err)

	assert.Equal(t, []string{"signal.question." + questionID}, pause.signals,
		"the message is the answer to the question its own thread is parked on")
	q, err := repo.GetQuestionByID(ctx, questionID)
	require.NoError(t, err)
	assert.Equal(t, db.QuestionStatusResolved, q.Status)
}

func TestSendMessage_FailedRunLeavesAnotherThreadsQuestionToItsThread(t *testing.T) {
	repo, ctx, fx, service, pause, questionID := setupFailedRunWithQuestion(t, func(absorbFixture) string { return "spawn-thread-" + uuid.NewString() })

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "carry on"))
	require.NoError(t, err)

	assert.Empty(t, pause.signals, "a message to the main thread is no answer to a sub-agent's question")
	assert.Equal(t, 1, pause.interruptResumes, "the run is resumed by reset-and-replay instead")
	q, err := repo.GetQuestionByID(ctx, questionID)
	require.NoError(t, err)
	assert.Equal(t, db.QuestionStatusPending, q.Status,
		"the sub-agent's question stays pending: the replayed run re-parks on it, for the user to answer there")
}
