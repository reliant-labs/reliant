// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/temporal/signaltest"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sub-agent that stopped on a failure — retries exhausted, a provider error,
// a usage limit — is not finished. Prod, workflow 9fbaace8 (2026-10-13): a
// sub-agent hit Anthropic's 7-day usage limit and ended "failed"; the user's
// message to it was refused with "This agent has already finished", so a
// transient condition had terminally killed the thread. A message to such an
// agent now resumes it, inside the run that is still going.

// stopSubAgent puts the fixture's child in the state a spawn ends in: its
// thread and its own workflow row both terminal with the given outcome.
func stopSubAgent(t *testing.T, repo *db.Repo, ctx context.Context, fx sendAgentMessageFixture, threadStatus int32, workflowStatus core.WorkflowStatus) {
	t.Helper()
	stoppedAt := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, fx.childThreadID, threadStatus, &stoppedAt)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.childThreadID, workflowStatus))
}

func TestSendAgentMessage_ResumesSubAgentThatStoppedOnAFailure(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())

	tc := &signaltest.Recorder{}
	service := &ChatService{database: repo, tempClient: tc, runs: runs.NewService(repo, tc, nil)}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.childThreadID,
		Message:  "switched you to another provider; carry on",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, "a sub-agent that failed must be resumable by a message, got: %s", resp.Msg.Message)
	assert.Contains(t, resp.Msg.Message, "resum", "the receipt must say the message resumes the agent")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
	assert.Equal(t, "switched you to another provider; carry on", queued[0].Body)

	// The doorbell is what resumes it: the run hears the agent has input and
	// relaunches it, and its first CallLLM drains the row.
	require.Len(t, tc.Signals, 1)
	assert.Equal(t, v2.ThreadWakeSignalName, tc.Signals[0].Name)
	assert.Equal(t, fx.chatID, tc.Signals[0].WorkflowID)
	sig, ok := tc.Signals[0].Arg.(v2.ThreadWakeSignal)
	require.True(t, ok)
	assert.Equal(t, fx.childThreadID, sig.Thread)
}

// A sub-agent the user stopped is finished: today's refusal stands.
func TestSendAgentMessage_SubAgentStoppedByTheUserStaysFinished(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusCancelled, db.Cancelled())

	tc := &signaltest.Recorder{}
	service := &ChatService{database: repo, tempClient: tc, runs: runs.NewService(repo, tc, nil)}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "keep going",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "already finished")
	assert.Equal(t, 0, countAgentMessageRows(t, rawDB, ctx, fx.childThreadID))
	assert.Empty(t, tc.Signals, "nothing was queued, so nothing is woken")
}

// A failed sub-agent whose chat run has since ended has no run to resume in.
// The refusal says why and what to do instead, and queues nothing.
func TestSendAgentMessage_StoppedSubAgentOfAnEndedRunIsRefusedWithGuidance(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Completed()))

	tc := &signaltest.Recorder{}
	service := &ChatService{database: repo, tempClient: tc, runs: runs.NewService(repo, tc, nil)}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "are you there?",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "stopped on an error")
	assert.Contains(t, resp.Msg.Message, "spawn(agent_id=", "the user must be told how the agent can still be resumed")
	assert.Equal(t, 0, countAgentMessageRows(t, rawDB, ctx, fx.childThreadID))
}

// A stopped agent has no next turn of its own: the doorbell is the only thing
// that resumes it. A doorbell that cannot be rung therefore cannot be papered
// over the way a live agent's can — the row is withdrawn and the user told,
// rather than left queued behind a receipt nothing will honour.
func TestSendAgentMessage_StoppedSubAgentThatCannotBeWokenIsNotLeftQueued(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())

	tc := &signaltest.Recorder{SignalErr: errors.New("workflow execution already completed")}
	service := &ChatService{database: repo, tempClient: tc, runs: runs.NewService(repo, tc, nil)}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "resume please",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "a message nothing will deliver must not stay queued")
}

// The model picker on a stopped sub-agent's thread must be usable: switching
// provider is how a user gets past a usage limit before resuming it. A
// finished agent has no next run for params to apply to.
func TestThreadAcceptsParamUpdate_StoppedSubAgentDoesAFinishedOneDoesNot(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &ChatService{database: repo}
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)

	require.NoError(t, service.threadAcceptsParamUpdate(ctx, chat, fx.childThreadID), "a running agent accepts param changes")

	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())
	require.NoError(t, service.threadAcceptsParamUpdate(ctx, chat, fx.childThreadID),
		"a stopped agent's next run is the one these params are for")

	// ...but only while there is a run to resume it in.
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Completed()))
	err = service.threadAcceptsParamUpdate(ctx, chat, fx.childThreadID)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "nothing will resume it once the chat's run has ended")
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Active()))

	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusCompleted, db.Completed())
	err = service.threadAcceptsParamUpdate(ctx, chat, fx.childThreadID)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "a finished agent has no next run")
}
