// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
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
func stopSubAgent(t *testing.T, repo *db.Repo, ctx context.Context, fx sendAgentMessageFixture, threadStatus db.ThreadStatus, workflowStatus core.WorkflowStatus) {
	t.Helper()
	stoppedAt := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, fx.childThreadID, threadStatus, &stoppedAt)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.childThreadID, workflowStatus))
}

func TestSendAgentMessage_ResumesSubAgentThatStoppedOnAFailure(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())

	tc := &signaltest.Recorder{}
	service := &Service{deps: Deps{Repo: repo, Temporal: tc}, runs: runs.NewService(repo, tc, nil)}
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
	t.Parallel()

	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusCancelled, db.Cancelled())

	tc := &signaltest.Recorder{}
	service := &Service{deps: Deps{Repo: repo, Temporal: tc}, runs: runs.NewService(repo, tc, nil)}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "keep going",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "already finished")
	assert.Equal(t, 0, countAgentMessageRows(t, rawDB, ctx, fx.childThreadID))
	assert.Empty(t, tc.Signals, "nothing was queued, so nothing is woken")
}

// stoppedSpawnRun records the spawn call that started the fixture's child, the
// durable half of "this agent stopped on a failure" a run resumes it from.
func stoppedSpawnRun(t *testing.T, repo *db.Repo, ctx context.Context, fx sendAgentMessageFixture, toolCallID string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, repo.UpsertToolCall(ctx, &db.ToolCall{
		ID: toolCallID, ChatID: fx.chatID, ThreadID: &fx.rootThreadID, ToolName: "spawn",
		Status: core.ToolCallStatusFailed, ChildWorkflowID: &fx.childThreadID,
		Input:       []byte(`{"preset":"general","prompt":"do the risky part"}`),
		RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}))
}

// The 9fbaace8 shape as users meet it: the sub-agent stopped, its parent read
// the failure and finished its turn, and the chat's run COMPLETED. The agent
// is still only stopped, so a message to it must resume it — by starting the
// chat's run to do so, since none is going. That run resumes the agent before
// anything else, and its root waits on the agent's report before taking a
// turn. (It used to be refused: "this chat's run has since ended".)
func TestSendAgentMessage_StoppedSubAgentOfAnEndedRunStartsARunThatResumesIt(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())
	stoppedSpawnRun(t, repo, ctx, fx, "tc-stopped")
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Completed()))
	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	workflowName := "builtin://agent"
	chat.WorkflowName = &workflowName
	require.NoError(t, repo.UpdateChat(ctx, chat))

	// The completed run's inputs as the chat's selection records them: its
	// successor continues with them. The closed run itself refuses a query.
	temporal := &closingRunTemporalClient{closed: true, lastInputs: map[string]interface{}{
		"model":              map[string]interface{}{"id": "mock"},
		v2.InputKeyLaunchRun: true,
	}}
	seedSelection(t, ctx, repo, fx.chatID, "test-user", workflowName, map[string]any{"model": map[string]any{"id": "mock"}})
	resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "the limit has reset; carry on",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, "STRANDED: a stopped agent of an idle chat must be resumable by a message, got: %s", resp.Msg.Message)
	assert.Contains(t, resp.Msg.Message, "restarted where it stopped")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the message waits in the agent's mailbox for its first turn")
	assert.Equal(t, "the limit has reset; carry on", queued[0].Body)

	started := temporal.runsStarted()
	require.Len(t, started, 1, "a run must be started to resume the agent")
	run := started[0]
	assert.Equal(t, fx.rootThreadID, run.options.ID, "the chat's own run, under its workflow ID")
	require.NotNil(t, run.input.ExecContext)
	assert.Equal(t, fx.rootThreadID, run.input.ExecContext.Thread)
	require.NotNil(t, run.input.Resume, "the chat's run is resumed, not started over at graph entry")
	assert.True(t, run.input.Resume.AwaitSpawnsFirst, "its root waits on the resumed agent before taking a turn")
	require.Len(t, run.input.Resume.ResumeStopped, 1)
	resumed := run.input.Resume.ResumeStopped[0]
	assert.Equal(t, "tc-stopped", resumed.ToolCallID)
	assert.Equal(t, fx.childThreadID, resumed.ChildThread)
	assert.Equal(t, fx.childThreadID, resumed.ChildWorkflowID)
	assert.Equal(t, fx.rootThreadID, resumed.ParentThread)
	assert.Equal(t, "general", resumed.Preset)
	assert.Equal(t, map[string]interface{}{"id": "mock"}, run.input.Inputs["model"], "it runs on what the completed run was running on")
	assert.NotContains(t, run.input.Inputs, v2.InputKeyLaunchRun, "it answers a person, not whatever launched the chat")
}

// A sub-agent whose parent sub-agent stopped on an error too reports to that
// parent, which is not running: it is resumed second. The user is told which
// agent to message first, and nothing is queued.
func TestSendAgentMessage_StoppedSubAgentOfAStoppedParentSaysToResumeTheParentFirst(t *testing.T) {
	t.Parallel()

	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())
	now := time.Now().UTC()
	grandchild := uuid.NewString()
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: grandchild, ChatID: fx.chatID, ParentThreadID: &fx.childThreadID,
		Origin: db.ThreadOriginSpawn, Status: db.ThreadStatusFailed, CreatedAt: now, WorkflowID: &grandchild,
	})
	require.NoError(t, err)

	tc := &signaltest.Recorder{}
	service := &Service{deps: Deps{Repo: repo, Temporal: tc}, runs: runs.NewService(repo, tc, nil)}
	resp, err := service.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: grandchild, Message: "carry on",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, fx.childThreadID, "the user must be told which agent to resume first")
	assert.Equal(t, 0, countAgentMessageRows(t, rawDB, ctx, grandchild))
	assert.Empty(t, tc.Signals)
}

// A stopped agent has no next turn of its own: the doorbell is the only thing
// that resumes it. A doorbell that cannot be rung therefore cannot be papered
// over the way a live agent's can — the row is withdrawn and the user told,
// rather than left queued behind a receipt nothing will honour.
func TestSendAgentMessage_StoppedSubAgentThatCannotBeWokenIsNotLeftQueued(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusFailed, db.Failed())

	// The run is open, but the doorbell does not reach it.
	temporal := &closingRunTemporalClient{wakeErr: errors.New("frontend unavailable")}
	resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
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
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	service := &Service{deps: Deps{Repo: repo}}
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
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "nothing will resume it once the chat's run has ended")
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Active()))

	stopSubAgent(t, repo, ctx, fx, db.ThreadStatusCompleted, db.Completed())
	err = service.threadAcceptsParamUpdate(ctx, chat, fx.childThreadID)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err), "a finished agent has no next run")
}
