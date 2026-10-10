// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"sync"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/queueddelivery"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/machinewait"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// endedRunTemporalClient is a chat's run that has ended in Temporal (closed,
// with history), until a run is started under its id.
type endedRunTemporalClient struct {
	client.Client

	mu         sync.Mutex
	status     enums.WorkflowExecutionStatus
	lastInputs map[string]interface{}
	started    []startedRun
	signals    []machinewait.Signal
	onStart    func()
}

func (c *endedRunTemporalClient) DescribeWorkflowExecution(_ context.Context, workflowID, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    c.status,
			Execution: &commonpb.WorkflowExecution{WorkflowId: workflowID, RunId: "run-ended"},
		},
	}, nil
}

func (c *endedRunTemporalClient) QueryWorkflow(_ context.Context, _, _, query string, _ ...interface{}) (converter.EncodedValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if query == "get_workflow_inputs" && c.lastInputs != nil {
		return jsonEncodedValue{v: c.lastInputs}, nil
	}
	return nil, serviceerror.NewQueryFailed("history no longer replays")
}

func (c *endedRunTemporalClient) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{}) (client.WorkflowRun, error) {
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
	c.status = enums.WORKFLOW_EXECUTION_STATUS_RUNNING
	return &fakeWorkflowRun{id: options.ID, runID: "run-continued"}, nil
}

func (c *endedRunTemporalClient) SignalWorkflow(_ context.Context, _, _, name string, arg interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := arg.(machinewait.Signal); ok && name == machinewait.SignalName {
		c.signals = append(c.signals, s)
	}
	return nil
}

func (c *endedRunTemporalClient) runsStarted() []startedRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]startedRun(nil), c.started...)
}

// resetPause is the reset-and-replay half of a resume. replays says whether
// the ended run's history still replays (reset succeeds) or diverged (a
// wedge: the caller restarts at the checkpoint).
type resetPause struct {
	repo    *db.Repo
	replays bool

	mu     sync.Mutex
	resets int
}

func (p *resetPause) PauseWorkflow(context.Context, string, string, string) error { return nil }
func (p *resetPause) ResumeWorkflow(context.Context, string, string) error        { return nil }
func (p *resetPause) SignalWithRecovery(context.Context, string, string, interface{}) error {
	return nil
}

func (p *resetPause) ResumeInterruptedWorkflow(ctx context.Context, workflowID, _ string) (string, error) {
	if !p.replays {
		return "", workflow.ErrReplayDiverged
	}
	p.mu.Lock()
	p.resets++
	p.mu.Unlock()
	// As PauseService does: the reset run is running.
	if err := p.repo.UpdateWorkflowStatus(ctx, workflowID, db.Active()); err != nil {
		return "", err
	}
	return "run-reset", nil
}

func (p *resetPause) resetCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resets
}

type continueFixture struct {
	ctx      context.Context
	repo     *db.Repo
	chatID   string
	threadID string
	temporal *endedRunTemporalClient
	pause    *resetPause
	svc      *ChatService
}

// newContinueFixture is a chat whose root run ended FAILED in the database
// and closed in Temporal, with the user's message as its latest turn.
func newContinueFixture(t *testing.T, replays bool) continueFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Failed())
	_, err := repo.SaveMessageToThread(ctx, fx.chatID, fx.rootThreadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER),
		"fix the failing test", &fx.rootThreadID, nil, nil)
	require.NoError(t, err)

	temporal := &endedRunTemporalClient{
		status: enums.WORKFLOW_EXECUTION_STATUS_FAILED,
		lastInputs: map[string]interface{}{
			"model":              map[string]interface{}{"id": "claude-opus"},
			v2.InputKeyLaunchRun: true,
		},
	}
	pause := &resetPause{repo: repo, replays: replays}
	svc := &ChatService{database: repo, tempClient: temporal, runs: runs.NewService(repo, temporal, pause)}
	return continueFixture{ctx: ctx, repo: repo, chatID: fx.chatID, threadID: fx.rootThreadID,
		temporal: temporal, pause: pause, svc: svc}
}

func (f continueFixture) queueForMachine(t *testing.T) {
	t.Helper()
	changed, err := f.repo.SetChatQueuedForMachine(f.ctx, f.chatID, true)
	require.NoError(t, err)
	require.True(t, changed)
}

func (f continueFixture) activity(t *testing.T) reliantv1.ChatActivity {
	t.Helper()
	chat, err := f.repo.GetChat(f.ctx, f.chatID)
	require.NoError(t, err)
	return reliantv1.ChatActivity(*chat.Activity)
}

// continueConcurrently calls ContinueQueued n times at once — the shape of a
// machine connect handled while the reconciler's sweep reaches the same chat —
// and returns how many reported starting a run.
func (f continueFixture) continueConcurrently(t *testing.T, n int) int {
	t.Helper()
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := f.svc.ContinueQueued(f.ctx, f.chatID)
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return started
}

// A message queued for a machine that failed to start is delivered when the
// machine is back — by resuming the run that ended, exactly as the user's own
// next send would — and only once, however many deliverers race for it.
func TestContinueQueued_MessageQueuedForTheMachineIsDeliveredOnce(t *testing.T) {
	f := newContinueFixture(t, true)
	f.queueForMachine(t)
	require.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, f.activity(t))

	started := f.continueConcurrently(t, 5)

	assert.Equal(t, 1, started, "exactly one deliverer starts a run")
	assert.Equal(t, 1, f.pause.resetCount(), "the ended run is resumed once")
	assert.Empty(t, f.temporal.runsStarted(), "a run whose history replays is resumed, not restarted")
	assert.NotEqual(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, f.activity(t), "nothing is queued any more")

	again, err := f.svc.ContinueQueued(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.False(t, again, "a later deliverer finds the run live")
	assert.Equal(t, 1, f.pause.resetCount())
}

// Chat 97654413: the user's "continue" was saved, and the run that took it in
// wedged and was ended by the reconciler. Its history no longer replays, so
// the message is delivered by a fresh run at the checkpoint — with the ended
// run's own inputs, so the model the user chose survives — once.
func TestContinueQueued_WedgedRunsUnansweredMessageStartsOneRunAtTheCheckpoint(t *testing.T) {
	f := newContinueFixture(t, false)
	now := time.Now().UTC()
	_, err := f.repo.UpdateThreadStatus(f.ctx, f.threadID, db.ThreadStatusFailed, &now)
	require.NoError(t, err)
	threadStatusAtStart := int32(-1)
	f.temporal.onStart = func() {
		thread, err := f.repo.GetThread(f.ctx, f.threadID)
		require.NoError(t, err)
		threadStatusAtStart = thread.Status
	}

	started := f.continueConcurrently(t, 5)

	assert.Equal(t, 1, started)
	runsStarted := f.temporal.runsStarted()
	require.Len(t, runsStarted, 1, "one run, never two")
	run := runsStarted[0]
	assert.Equal(t, f.chatID, run.options.ID)
	assert.Equal(t, f.threadID, run.input.ExecContext.Thread)
	assert.NotNil(t, run.input.Resume, "it resumes at the checkpoint, not at graph entry")
	assert.Equal(t, map[string]interface{}{"id": "claude-opus"}, run.input.Inputs["model"], "the ended run's own inputs")
	assert.NotContains(t, run.input.Inputs, v2.InputKeyLaunchRun)
	assert.Equal(t, db.ThreadStatusRunning, threadStatusAtStart,
		"the thread is revived before the run starts, so the mailbox sweep cannot take its queued rows")

	wf, err := f.repo.GetWorkflow(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), wf.Status, "recorded running before the lock is released")
}

// A message queued in the mailbox (sent while the run was live, never drained)
// is just as owed as one in history.
func TestContinueQueued_QueuedMailboxRowIsOwed(t *testing.T) {
	f := newContinueFixture(t, false)
	_, err := f.repo.SaveMessageToThread(f.ctx, f.chatID, f.threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		"done", &f.threadID, nil, nil)
	require.NoError(t, err)
	require.NoError(t, f.repo.EnqueueAgentMessage(f.ctx, &db.AgentMessage{
		ID: "queued-1", FromThreadID: f.threadID, ChatID: f.chatID, ToThreadID: f.threadID,
		Kind: core.AgentMessageKindHumanMessage, Body: "continue", Status: core.AgentMessageStatusQueued, CreatedAt: time.Now(),
	}))

	started, err := f.svc.ContinueQueued(f.ctx, f.chatID)

	require.NoError(t, err)
	assert.True(t, started)
	assert.Len(t, f.temporal.runsStarted(), 1)
}

// A run that ended after answering, with nothing queued, is owed nothing.
func TestContinueQueued_NothingOwedStartsNothing(t *testing.T) {
	f := newContinueFixture(t, false)
	_, err := f.repo.SaveMessageToThread(f.ctx, f.chatID, f.threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT),
		"all fixed", &f.threadID, nil, nil)
	require.NoError(t, err)
	_, err = f.repo.SaveMessageToThread(f.ctx, f.chatID, f.threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		"This conversation's workflow was interrupted", &f.threadID, nil, nil)
	require.NoError(t, err)

	started, err := f.svc.ContinueQueued(f.ctx, f.chatID)

	require.NoError(t, err)
	assert.False(t, started, "a system note after the reply does not make the user's message unanswered")
	assert.Empty(t, f.temporal.runsStarted())
	assert.Zero(t, f.pause.resetCount())
}

// A run the user stopped is not resumed behind their back; the stale marker
// goes.
func TestContinueQueued_StoppedRunIsNotResumed(t *testing.T) {
	f := newContinueFixture(t, true)
	f.queueForMachine(t)
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Cancelled()))

	started, err := f.svc.ContinueQueued(f.ctx, f.chatID)

	require.NoError(t, err)
	assert.False(t, started)
	assert.Zero(t, f.pause.resetCount())
	assert.NotEqual(t, reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE, f.activity(t))
}

// "Continue without machine" moves the conversation to a branch. The chat it
// leaves must not answer the same message later: its queued message is dropped
// and a run still waiting for the machine is told to stop.
func TestAbandonMachineWait_DropsTheQueueAndEndsTheWait(t *testing.T) {
	f := newContinueFixture(t, true)
	f.queueForMachine(t)
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Active()))
	require.NoError(t, f.repo.SetChatDaemonBlocked(f.ctx, f.chatID, true))
	chat, err := f.repo.GetChat(f.ctx, f.chatID)
	require.NoError(t, err)

	f.svc.abandonMachineWait(f.ctx, chat)

	require.Len(t, f.temporal.signals, 1)
	assert.True(t, f.temporal.signals[0].Abandon)
	cleared, err := f.repo.SetChatQueuedForMachine(f.ctx, f.chatID, false)
	require.NoError(t, err)
	assert.False(t, cleared, "the queued message was already dropped")
}

// A run doing anything other than waiting for its machine is not stopped by
// branching away from it.
func TestAbandonMachineWait_LeavesARunThatIsNotWaiting(t *testing.T) {
	f := newContinueFixture(t, true)
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Active()))
	chat, err := f.repo.GetChat(f.ctx, f.chatID)
	require.NoError(t, err)

	f.svc.abandonMachineWait(f.ctx, chat)

	assert.Empty(t, f.temporal.signals)
}

// A second message sent while the run waits for its machine must not make the
// run look like it is thinking: the waiting marker stays (it still describes
// the run), and the run is asked to check the machine now — the send just
// woke it — instead of at its next recheck, which can be minutes away.
func TestSendMessage_WhileTheRunWaitsForItsMachine_KeepsTheWaitAndNudgesIt(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	require.NoError(t, repo.SetChatDaemonBlocked(ctx, fx.chatID, true))
	temporal := &endedRunTemporalClient{status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING}
	svc := &ChatService{database: repo, tempClient: temporal, runs: runs.NewService(repo, temporal, &resetPause{repo: repo})}

	_, err := svc.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "and also this"))
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, reliantv1.ChatActivity_CHAT_ACTIVITY_WAITING_FOR_DAEMON, reliantv1.ChatActivity(*chat.Activity),
		"the run is still waiting for its machine, and still reads that way")
	require.Len(t, temporal.signals, 1, "the waiting run is asked to check its machine now")
	assert.False(t, temporal.signals[0].Abandon)
}

// onlineMachines reports every machine connected.
type onlineMachines struct{}

func (onlineMachines) IsDaemonOnline(context.Context, string, *toolexec.DaemonSelector) (bool, error) {
	return true, nil
}

// End to end through delivery: the run ended because the machine failed to
// start (its preflight queued the message), the user hit Try again, and the
// machine connected. The connect starts the run that delivers the message —
// no resend — and a second connect (or the sweep) delivers nothing more.
func TestMachineConnected_DeliversTheMessageQueuedForAFailedMachineOnce(t *testing.T) {
	f := newContinueFixture(t, true)
	f.queueForMachine(t)
	delivery := queueddelivery.New(f.repo, onlineMachines{}, f.svc, f.temporal)

	assert.Equal(t, 1, delivery.MachineConnected(f.ctx, "test-user"))
	assert.Equal(t, 0, delivery.MachineConnected(f.ctx, "test-user"), "a second connect finds nothing queued")
	swept, err := delivery.Sweep(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, swept)
	assert.Equal(t, 1, f.pause.resetCount(), "delivered exactly once")
}
