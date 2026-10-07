// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closingRunTemporalClient is a chat's run that is open when the send path
// checks it, and has CLOSED by the time the send path rings its doorbell —
// the window between the status read and the wake. Signalling a closed
// workflow is what Temporal answers NotFound to.
type closingRunTemporalClient struct {
	client.Client

	mu sync.Mutex
	// closeOnWake makes the first thread-wake find the run gone; onClose runs
	// as it closes (the closed run's own bookkeeping). With closeOnWake false,
	// wakeErr makes the wake fail while the run stays open.
	closeOnWake bool
	onClose     func()
	wakeErr     error
	closed      bool
	wakes       int
	// lastInputs is what the run's get_workflow_inputs query answers, open or
	// closed (Temporal answers both); onStart runs as a run is started.
	lastInputs map[string]interface{}
	onStart    func()
	started    []startedRun
}

type startedRun struct {
	options client.StartWorkflowOptions
	input   v2.WorkflowInput
}

func (c *closingRunTemporalClient) DescribeWorkflowExecution(
	_ context.Context, workflowID, _ string,
) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := enums.WORKFLOW_EXECUTION_STATUS_RUNNING
	if c.closed {
		status = enums.WORKFLOW_EXECUTION_STATUS_COMPLETED
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    status,
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-1"},
		},
	}, nil
}

func (c *closingRunTemporalClient) SignalWorkflow(_ context.Context, _, _, name string, _ interface{}) error {
	c.mu.Lock()
	if name != v2.ThreadWakeSignalName {
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return serviceerror.NewNotFound("workflow execution already completed")
		}
		return nil
	}
	c.wakes++
	if !c.closed && c.closeOnWake {
		c.closed = true
		onClose := c.onClose
		c.mu.Unlock()
		if onClose != nil {
			onClose()
		}
		return serviceerror.NewNotFound("workflow execution already completed")
	}
	defer c.mu.Unlock()
	if c.closed {
		return serviceerror.NewNotFound("workflow execution already completed")
	}
	return c.wakeErr
}

func (c *closingRunTemporalClient) ExecuteWorkflow(
	_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	if c.onStart != nil {
		c.onStart()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run := startedRun{options: options}
	if len(args) == 1 {
		run.input, _ = args[0].(v2.WorkflowInput)
	}
	c.started = append(c.started, run)
	return &fakeWorkflowRun{id: options.ID, runID: "run-new"}, nil
}

func (c *closingRunTemporalClient) QueryWorkflow(_ context.Context, _, _, query string, _ ...interface{}) (converter.EncodedValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if query == "get_workflow_inputs" && c.lastInputs != nil {
		return jsonEncodedValue{v: c.lastInputs}, nil
	}
	return nil, assertNotFound{}
}

func (c *closingRunTemporalClient) runsStarted() []startedRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]startedRun(nil), c.started...)
}

type jsonEncodedValue struct{ v interface{} }

func (e jsonEncodedValue) HasValue() bool { return e.v != nil }
func (e jsonEncodedValue) Get(ptr interface{}) error {
	raw, err := json.Marshal(e.v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, ptr)
}

func lateMessageService(repo *db.Repo, temporal *closingRunTemporalClient) *ChatService {
	return &ChatService{database: repo, tempClient: temporal, runs: runs.NewService(repo, temporal, nil)}
}

func countOf(bodies []string, body string) int {
	n := 0
	for _, b := range bodies {
		if b == body {
			n++
		}
	}
	return n
}

// The run is open when SendMessage reads it, so the message is queued for the
// thread and the doorbell rung — and the run has closed in between, stamping
// the thread terminal, so the doorbell reaches nothing. No turn of the closed
// run will read the message, and the old code only logged the failed wake: the
// message sat unanswered until the user sent another. A run must be started
// for it, and the thread revived BEFORE that run starts, so the reconciler's
// sweep cannot mark the queued row undelivered in between.
func TestSendMessage_RunThatClosesBeforeItsWakeGetsARunStarted(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	threadStatusAtStart := int32(-1)
	temporal := &closingRunTemporalClient{
		closeOnWake: true,
		onClose: func() {
			now := time.Now().UTC()
			_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, db.ThreadStatusCompleted, &now)
			require.NoError(t, err)
		},
	}
	temporal.onStart = func() {
		thread, err := repo.GetThread(ctx, fx.rootThreadID)
		require.NoError(t, err)
		threadStatusAtStart = thread.Status
	}

	resp, err := lateMessageService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "and one more thing"))
	require.NoError(t, err)

	started := temporal.runsStarted()
	require.Len(t, started, 1,
		"STRANDED: the run closed before it could be woken and no run was started for the message")
	assert.Equal(t, fx.chatID, started[0].options.ID, "the chat's own workflow ID, so it replaces the closed run")
	assert.Equal(t, fx.rootThreadID, started[0].input.ExecContext.Thread, "on the thread the message was queued to")
	assert.Nil(t, started[0].input.Resume, "a fresh run at graph entry")
	assert.Equal(t, "run-new", resp.Msg.RunId)
	assert.True(t, resp.Msg.Queued, "the row is still queued; the new run's first turn drains it")
	assert.Equal(t, db.ThreadStatusRunning, threadStatusAtStart,
		"the thread must be revived before the run starts, or the sweep can mark the row undelivered in between")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "queued once, for the new run's first call_llm — not also written to history")
	assert.Equal(t, "and one more thing", queued[0].Body)
	assert.Zero(t, countOf(transcriptBodies(t, ctx, repo, fx.chatID), "and one more thing"))
}

// The control: the doorbell reached the run, which will therefore read the
// message (the run gives a late wake a turn), so no second run is started over
// it.
func TestSendMessage_RunThatTakesItsWakeGetsNoSecondRun(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &closingRunTemporalClient{}

	_, err := lateMessageService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "steer"))
	require.NoError(t, err)
	assert.Empty(t, temporal.runsStarted())
	assert.Equal(t, 1, temporal.wakes)
}

// A doorbell that fails while the run is still open is a transient failure,
// not a closed run: starting a run then would terminate the live one
// (TERMINATE_EXISTING). It is rung once more instead.
func TestSendMessage_TransientWakeFailureOnAnOpenRunRetriesTheWake(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &closingRunTemporalClient{wakeErr: serviceerror.NewUnavailable("frontend restarting")}

	_, err := lateMessageService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "steer"))
	require.NoError(t, err)
	assert.Empty(t, temporal.runsStarted(), "a live run must not be replaced")
	assert.Equal(t, 2, temporal.wakes, "the doorbell is rung again")
}

// The queued path through the same window: the composer queues into the
// chat's main thread while its run is open, and the run closes — stamping the
// thread terminal — before the doorbell. The row would sit queued until the
// reconciler marked it undelivered. A run must be started that drains it, and
// the thread revived FIRST, so the sweep never sees a terminal thread with
// mail in it while that run is on its way.
func TestSendAgentMessage_RunThatClosesBeforeItsWakeGetsARunStarted(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	threadStatusAtStart := int32(-1)
	temporal := &closingRunTemporalClient{
		closeOnWake: true,
		onClose: func() {
			now := time.Now().UTC()
			_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, db.ThreadStatusCompleted, &now)
			require.NoError(t, err)
		},
		lastInputs: map[string]interface{}{
			"model":              map[string]interface{}{"id": "mock"},
			v2.InputKeyLaunchRun: true,
		},
	}
	temporal.onStart = func() {
		thread, err := repo.GetThread(ctx, fx.rootThreadID)
		require.NoError(t, err)
		threadStatusAtStart = thread.Status
	}

	resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.rootThreadID,
		Message:  "queued as it finished",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, resp.Msg.Message)

	started := temporal.runsStarted()
	require.Len(t, started, 1, "STRANDED: the row was left queued for a run that had closed, and no run started")
	assert.Equal(t, fx.rootThreadID, started[0].input.ExecContext.Thread)
	assert.Equal(t, map[string]interface{}{"id": "mock"}, started[0].input.Inputs["model"], "the closed run's own inputs")
	assert.NotContains(t, started[0].input.Inputs, v2.InputKeyLaunchRun, "a run a person's message started is not the launch run")
	assert.Equal(t, db.ThreadStatusRunning, threadStatusAtStart,
		"the thread must be revived before the run starts, or the sweep can mark the row undelivered in between")

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the row stays queued for the new run's first call_llm to drain")
	assert.Equal(t, "queued as it finished", queued[0].Body)
}

// The composer routes a send to its queue for as long as it shows the run as
// working, and at the instant a run finishes it still does. On the browser
// check, a send 0 ms after the reply ended went to SendAgentMessage, which
// refused it ("This agent has already finished") and left it in the composer
// for the user to send a second time. A message to the chat's MAIN thread
// after its run ended is a message to the chat, and gets what SendMessage
// gives one: a run started to answer it, the run the ended one would have
// continued as.
func TestSendAgentMessage_EndedMainRunGetsARunStarted(t *testing.T) {
	for _, tc := range []struct {
		name         string
		runStatus    db.WorkflowStatus
		threadStatus int32
		// stillOpen is a run Temporal has not closed yet.
		stillOpen bool
	}{
		{name: "completed run, thread stamped completed", runStatus: db.Completed(), threadStatus: db.ThreadStatusCompleted},
		// The live-DB shape: the thread row never moved, only the run's did.
		{name: "completed run, thread still reads running", runStatus: db.Completed(), threadStatus: db.ThreadStatusRunning},
		// Stopped by the user and still stopping. A stopping run exits without
		// reading its doorbell, so the run is started without ringing it.
		{name: "cancelled run that is still stopping", runStatus: db.Cancelled(), threadStatus: db.ThreadStatusCancelled, stillOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)
			ctx, fx := setupAbsorbFixture(t, repo, "test-user", tc.runStatus)
			if tc.threadStatus != db.ThreadStatusRunning {
				now := time.Now().UTC()
				_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, tc.threadStatus, &now)
				require.NoError(t, err)
			}
			threadStatusAtStart := int32(-1)
			temporal := &closingRunTemporalClient{
				closed: !tc.stillOpen,
				lastInputs: map[string]interface{}{
					"model":              map[string]interface{}{"id": "mock"},
					v2.InputKeyLaunchRun: true,
				},
			}
			temporal.onStart = func() {
				thread, err := repo.GetThread(ctx, fx.rootThreadID)
				require.NoError(t, err)
				threadStatusAtStart = thread.Status
			}

			resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
				ChatId:   fx.chatID,
				ThreadId: fx.rootThreadID,
				Message:  "sent as the reply ended",
			}))
			require.NoError(t, err)
			require.True(t, resp.Msg.Success,
				"SENT TWICE: refused, so the message stays in the composer until the user sends it again: %s", resp.Msg.Message)

			started := temporal.runsStarted()
			require.Len(t, started, 1, "no run was started for the message")
			assert.Equal(t, fx.chatID, started[0].options.ID, "the chat's own workflow ID")
			assert.Equal(t, fx.rootThreadID, started[0].input.ExecContext.Thread)
			assert.Nil(t, started[0].input.Resume, "a fresh run at graph entry")
			assert.Equal(t, map[string]interface{}{"id": "mock"}, started[0].input.Inputs["model"], "the ended run's own inputs")
			assert.NotContains(t, started[0].input.Inputs, v2.InputKeyLaunchRun, "a run a person's message started is not the launch run")
			assert.Equal(t, db.ThreadStatusRunning, threadStatusAtStart,
				"the thread must be revived before the run starts, or the sweep can mark the row undelivered in between")
			if tc.stillOpen {
				assert.Zero(t, temporal.wakes, "a stopping run never reads its doorbell; ringing it instead would strand the row")
			}

			queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
			require.NoError(t, err)
			require.Len(t, queued, 1, "queued once, for the new run's first call_llm to drain")
			assert.Equal(t, "sent as the reply ended", queued[0].Body)
		})
	}
}

// The control for a completed run: Temporal has not closed it yet, so it is
// inside its end-of-run check, which reads the doorbell and continues as a
// fresh run for it (late_wake.go). Starting a second run would terminate it
// (TERMINATE_EXISTING), so the row is queued and the doorbell rung, as for a
// live run.
func TestSendAgentMessage_CompletedMainRunStillFinishingTakesTheWake(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	now := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, db.ThreadStatusCompleted, &now)
	require.NoError(t, err)
	temporal := &closingRunTemporalClient{lastInputs: map[string]interface{}{"model": map[string]interface{}{"id": "mock"}}}

	resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID, Message: "sent as it finishes",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, resp.Msg.Message)
	assert.Empty(t, temporal.runsStarted(), "a run that will read the wake must not be replaced")
	assert.Equal(t, 1, temporal.wakes)
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1)
}

// A failed run is not started over from here. SendMessage is what recovers a
// failed run — it resumes it where it stopped — and a fresh run from graph
// entry would bypass that, so the queue still refuses and the composer keeps
// the text for a normal send.
func TestSendAgentMessage_FailedMainRunIsStillRefused(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	now := time.Now().UTC()
	_, err := repo.UpdateThreadStatus(ctx, fx.rootThreadID, db.ThreadStatusFailed, &now)
	require.NoError(t, err)
	temporal := &closingRunTemporalClient{closed: true, lastInputs: map[string]interface{}{"model": map[string]interface{}{"id": "mock"}}}

	resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID, Message: "after a failure",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Empty(t, temporal.runsStarted())
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "nothing is left queued behind a refusal")
}

// If the closed run's inputs cannot be read, there is no run to start, and
// the receipt must not promise one: the row is withdrawn and the user told to
// send it as a message (the composer keeps the text on a refusal).
func TestSendAgentMessage_RunThatClosesWithNoReadableInputsIsRefusedHonestly(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &closingRunTemporalClient{closeOnWake: true}

	resp, err := lateMessageService(repo, temporal).SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID, Message: "queued as it finished",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, "Send it as a normal message")
	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Empty(t, queued, "nothing is left queued behind a refusal")
	assert.Empty(t, temporal.runsStarted())
}
