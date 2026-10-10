// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	llmdrivers "github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A send whose params SendMessage refuses must leave nothing behind.
//
// Prod chat 66a045ce (user 30542add, 2026-10-10) answered "continue" on a
// paused run and got `invalid_argument: workflow input validation failed:
// input 'model': …` — after the resume path had already saved the message.
// The client was told the send failed, the transcript gained a turn no run
// would read until some later send, and the retry saved it a second time. #685
// stopped refusing that particular input (a model no connected provider can
// serve now moves to one that can), but every input SendMessage still refuses
// — a model the registry no longer knows, a malformed selector, a user with no
// provider at all, a missing required input — was refused the same way, after
// the write, on every path.
//
// Refusing a send has to mean the send did not happen: no transcript row, no
// mailbox row, no signal, no resume, no new run and no status change. Each
// status SendMessage routes on is pinned separately, because each branch
// persists in its own way.
func TestSendMessage_RejectedParamsPersistNothing(t *testing.T) {
	cases := []struct {
		name            string
		dbStatus        db.WorkflowStatus
		temporalStatus  enums.WorkflowExecutionStatus
		gone            bool // Temporal has no such execution
		pauseController runs.PauseController
	}{
		{
			// The 66a045ce case: "continue" on a paused run.
			name:            "paused run",
			dbStatus:        db.Paused(),
			temporalStatus:  enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
			pauseController: &recordingPauseController{},
		},
		{
			name:           "running run",
			dbStatus:       db.Active(),
			temporalStatus: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
		{
			name:           "finished run (send starts a new one)",
			dbStatus:       db.Completed(),
			temporalStatus: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		},
		{
			// Reset-and-replay saves the message before it knows whether it
			// can replay, then falls back to a fresh run.
			name:            "failed run (reset-and-replay)",
			dbStatus:        db.Failed(),
			temporalStatus:  enums.WORKFLOW_EXECUTION_STATUS_TERMINATED,
			pauseController: &recordingPauseController{},
		},
		{
			// Running in the database, gone from Temporal: ghost recovery.
			name:     "lost run (ghost recovery)",
			dbStatus: db.Active(),
			gone:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)

			ctx, fx := setupAbsorbFixture(t, repo, "test-user", tc.dbStatus)
			connectOnlyAnthropic(t, ctx, repo, "test-user")
			temporal := &rejectedSendTemporalClient{
				wakeTestTemporalClient: wakeTestTemporalClient{
					absorbTestTemporalClient: absorbTestTemporalClient{exists: !tc.gone, status: tc.temporalStatus},
				},
			}
			service := &ChatService{
				database:   repo,
				tempClient: temporal,
				runs:       runs.NewService(repo, temporal, tc.pauseController),
			}

			_, err := service.SendMessage(ctx, sendWithUnknownModel(t, fx.chatID, "continue"))
			require.Error(t, err, "a model the registry does not know must be refused")
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), retiredModelID,
				"the refusal must name the model the user has to change")

			assert.Empty(t, transcriptBodies(t, ctx, repo, fx.chatID),
				"a refused send must not leave its message in the transcript")
			queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
			require.NoError(t, err)
			assert.Empty(t, queued, "a refused send must not queue its message for the run")
			assert.Empty(t, temporal.signals, "a refused send must not signal the run")
			assert.Zero(t, temporal.started, "a refused send must not start a run")
			if pc, ok := tc.pauseController.(*recordingPauseController); ok {
				assert.Zero(t, pc.resumes, "a refused send must not resume the run")
			}

			wf, err := repo.GetWorkflow(ctx, fx.rootThreadID)
			require.NoError(t, err)
			assert.Equal(t, tc.dbStatus, wf.Status, "a refused send must not move the run's status")
		})
	}
}

// connectOnlyAnthropic gives the user one connected provider, as the prod user
// had (Claude, no Codex), so the model check runs for real rather than
// failing on "no provider" first. No provider catalog is consulted:
// availability falls back to "every configured provider serves".
func connectOnlyAnthropic(t *testing.T, ctx context.Context, repo *db.Repo, userID string) {
	t.Helper()
	llmdrivers.InitializeAPIKeyProvider(repo)
	original := llmdrivers.AccountAvailabilityClient
	llmdrivers.AccountAvailabilityClient = func(context.Context, models.DriverID, models.DriverConfig) (registry.Client, error) {
		return nil, errors.New("no provider catalog in this test")
	}
	t.Cleanup(func() { llmdrivers.AccountAvailabilityClient = original })
	require.NoError(t, repo.SetProviderAPIKey(ctx, userID, "anthropic", "test-key-anthropic"))
}

// retiredModelID is a model the registry does not know — what a composer
// still holds for a chat after its model is retired. Unlike a known model on
// a disconnected provider (moved by #685), nothing can stand in for it, so
// SendMessage still refuses it.
const retiredModelID = "gpt-4.1-retired@codex"

// sendWithUnknownModel is the composer's send while it still holds a model
// the registry no longer knows.
func sendWithUnknownModel(t *testing.T, chatID, content string) *connect.Request[reliantv1.SendMessageRequest] {
	t.Helper()
	return connect.NewRequest(&reliantv1.SendMessageRequest{
		ChatId: chatID,
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: content,
		}},
		WorkflowParams: map[string]*structpb.Value{
			"model": mustStructValue(t, map[string]interface{}{
				"id":   retiredModelID,
				"tags": []interface{}{"flagship"},
			}),
		},
	})
}

// rejectedSendTemporalClient records signals (via wakeTestTemporalClient) and
// counts run starts, so a test can assert a refused send touched neither.
type rejectedSendTemporalClient struct {
	wakeTestTemporalClient
	started int
}

func (c *rejectedSendTemporalClient) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	c.started++
	return c.wakeTestTemporalClient.ExecuteWorkflow(ctx, options, workflow, args...)
}

// recordingPauseController counts resumes of either kind and otherwise
// succeeds.
type recordingPauseController struct {
	succeedingPauseController
	resumes int
}

func (p *recordingPauseController) ResumeWorkflow(ctx context.Context, workflowID, chatID string) error {
	p.resumes++
	return p.succeedingPauseController.ResumeWorkflow(ctx, workflowID, chatID)
}

func (p *recordingPauseController) ResumeInterruptedWorkflow(ctx context.Context, workflowID, chatID string) (string, error) {
	p.resumes++
	return p.succeedingPauseController.ResumeInterruptedWorkflow(ctx, workflowID, chatID)
}
