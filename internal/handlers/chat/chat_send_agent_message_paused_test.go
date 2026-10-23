// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
)

// A message queued to a paused chat resumes it. Prod, chat adcee226
// (2026-10-21): the user paused the chat, typed "continue" to the root — the
// composer queues a send to a run it shows as working — and the run stayed
// paused with the message unread, because SendAgentMessage counted a paused
// run as live and only rang the thread's doorbell, which does not open the
// pause gate.

// orderedPauseController records each resume into the same log the Temporal
// fake records signals in, so a test can see the resume came before the wake.
type orderedPauseController struct {
	succeedingPauseController
	log       *[]string
	resumeErr error
	resumed   []string
}

func (p *orderedPauseController) ResumeWorkflow(_ context.Context, workflowID, _ string, _ map[string]interface{}) error {
	if p.resumeErr != nil {
		return p.resumeErr
	}
	p.resumed = append(p.resumed, workflowID)
	*p.log = append(*p.log, "resume:"+workflowID)
	return nil
}

type orderedSignalClient struct {
	wakeTestTemporalClient
	log *[]string
}

func (c *orderedSignalClient) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error {
	if wake, ok := arg.(v2.ThreadWakeSignal); ok {
		*c.log = append(*c.log, "wake:"+wake.Thread)
	}
	return c.wakeTestTemporalClient.SignalWorkflow(ctx, workflowID, runID, signalName, arg)
}

type pausedAgentMessageFixture struct {
	sendAgentMessageFixture
	ctx      context.Context
	repo     *db.Repo
	service  *Service
	pause    *orderedPauseController
	temporal *orderedSignalClient
	log      *[]string
}

// setupPausedAgentMessageFixture is a chat paused the way PauseChat leaves
// one: the root's row and the live sub-agent's row both stopped/paused, the
// Temporal execution still open.
func setupPausedAgentMessageFixture(t *testing.T) pausedAgentMessageFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.rootThreadID, db.Paused()))
	require.NoError(t, repo.UpdateWorkflowStatus(ctx, fx.childThreadID, db.Paused()))

	var log []string
	temporal := &orderedSignalClient{
		wakeTestTemporalClient: wakeTestTemporalClient{
			absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
		},
		log: &log,
	}
	pause := &orderedPauseController{log: &log}
	service := &Service{
		deps: Deps{Repo: repo, Temporal: temporal},
		runs: runs.NewService(repo, temporal, pause),
	}
	return pausedAgentMessageFixture{
		sendAgentMessageFixture: fx,
		ctx:                     ctx, repo: repo, service: service, pause: pause, temporal: temporal, log: &log,
	}
}

func (fx pausedAgentMessageFixture) send(t *testing.T, threadID, body string) *reliantv1.SendAgentMessageResponse {
	t.Helper()
	resp, err := fx.service.SendAgentMessage(fx.ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: threadID, Message: body,
	}))
	require.NoError(t, err)
	return resp.Msg
}

func TestSendAgentMessage_PausedChatIsResumedForAMessageToItsRoot(t *testing.T) {
	t.Parallel()

	fx := setupPausedAgentMessageFixture(t)

	resp := fx.send(t, fx.rootThreadID, "continue")
	require.True(t, resp.Success, "got: %s", resp.Message)

	assert.Equal(t, []string{fx.rootThreadID}, fx.pause.resumed,
		"a message to a paused chat must resume its run — the doorbell alone does not open the pause gate")
	assert.Equal(t, []string{"resume:" + fx.rootThreadID, "wake:" + fx.rootThreadID}, *fx.log,
		"resume first, then the doorbell: a thread parked on its own wait wakes only for the doorbell")

	root, err := fx.repo.GetWorkflow(fx.ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status, "the resumed run reads as running")

	queued, err := fx.repo.ListQueuedAgentMessagesForThread(fx.ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the message stays queued for the resumed run's next call_llm")
	assert.Equal(t, "continue", queued[0].Body)
}

// The pause is the chat's, not the thread's: every thread runs in the chat's
// one execution. A message to a paused sub-agent resumes that execution.
func TestSendAgentMessage_PausedChatIsResumedForAMessageToASubAgent(t *testing.T) {
	t.Parallel()

	fx := setupPausedAgentMessageFixture(t)

	resp := fx.send(t, fx.childThreadID, "continue")
	require.True(t, resp.Success, "got: %s", resp.Message)

	assert.Equal(t, []string{fx.rootThreadID}, fx.pause.resumed,
		"the chat's run is resumed under the root's workflow id")
	assert.Equal(t, []string{"resume:" + fx.rootThreadID, "wake:" + fx.childThreadID}, *fx.log,
		"the doorbell still names the sub-agent: it is the thread with input")

	queued, err := fx.repo.ListQueuedAgentMessagesForThread(fx.ctx, fx.childThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)

	wakes := threadWakeSignals(fx.temporal.signals)
	require.Len(t, wakes, 1)
	sig, ok := wakes[0].Arg.(v2.ThreadWakeSignal)
	require.True(t, ok)
	assert.True(t, sig.FromUser,
		"the doorbell must say a person sent it: a thread waiting on an ask_user question yields only to the user's input")
}

// A run that is not paused is not resumed: a live run's doorbell is its whole
// delivery.
func TestSendAgentMessage_RunningChatIsOnlyWoken(t *testing.T) {
	t.Parallel()

	fx := setupPausedAgentMessageFixture(t)
	require.NoError(t, fx.repo.UpdateWorkflowStatus(fx.ctx, fx.rootThreadID, db.Active()))
	require.NoError(t, fx.repo.UpdateWorkflowStatus(fx.ctx, fx.childThreadID, db.Active()))

	resp := fx.send(t, fx.childThreadID, "look at the tests too")
	require.True(t, resp.Success, "got: %s", resp.Message)

	assert.Empty(t, fx.pause.resumed, "a running chat must not be resumed")
	assert.Equal(t, []string{"wake:" + fx.childThreadID}, *fx.log)
}

// A paused run that cannot be resumed in place (gone from Temporal) is not
// promised a delivery: the row is withdrawn and the user told to send the
// message to the chat, whose SendMessage recovers the run at its position.
func TestSendAgentMessage_PausedChatThatCannotResumeWithdrawsTheMessage(t *testing.T) {
	t.Parallel()

	fx := setupPausedAgentMessageFixture(t)
	fx.pause.resumeErr = fmt.Errorf("resume: %w", workflow.ErrWorkflowNotFound)

	resp := fx.send(t, fx.childThreadID, "continue")
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Message, "Send this as a normal message")

	queued, err := fx.repo.ListQueuedAgentMessagesForThread(fx.ctx, fx.childThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "a message nothing will deliver must not stay queued")
	assert.Empty(t, threadWakeSignals(fx.temporal.signals), "nothing is woken for a withdrawn message")
}

// The answer to an async question (the question service sends it under
// threadwake.WithoutQuestionYield) rings a doorbell that is not the user's: it
// answers that question, so a thread parked on a different, blocking one must
// keep waiting on it rather than close it unanswered.
func TestSendAgentMessage_AsyncAnswerDoorbellDoesNotYieldQuestions(t *testing.T) {
	t.Parallel()

	fx := setupPausedAgentMessageFixture(t)
	require.NoError(t, fx.repo.UpdateWorkflowStatus(fx.ctx, fx.rootThreadID, db.Active()))
	require.NoError(t, fx.repo.UpdateWorkflowStatus(fx.ctx, fx.childThreadID, db.Active()))

	resp, err := fx.service.SendAgentMessage(threadwake.WithoutQuestionYield(fx.ctx), connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.childThreadID, Message: "Answer to the question you asked without waiting",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, "got: %s", resp.Msg.Message)

	wakes := threadWakeSignals(fx.temporal.signals)
	require.Len(t, wakes, 1, "the thread is still woken, like any mail")
	sig, ok := wakes[0].Arg.(v2.ThreadWakeSignal)
	require.True(t, ok)
	assert.Equal(t, fx.childThreadID, sig.Thread)
	assert.False(t, sig.FromUser, "an async answer's doorbell must not make a blocking question's wait yield")
}
