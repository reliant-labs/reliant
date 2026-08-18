package threads

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// InterruptThread is the "stop, this changes things" verb (see
// specs/thread-interrupt.md). These tests pin what it stops, what it must NOT
// stop, and the honesty of what it reports at the service boundary.

type interruptFixture struct {
	userID        string
	chatID        string
	rootThreadID  string
	childThreadID string
}

type recordedThreadInterruptSignal struct {
	workflowID string
	runID      string
	signalName string
	signalArg  interface{}
}

type recordingThreadInterruptSignaler struct {
	mu      sync.Mutex
	signals []recordedThreadInterruptSignal
	failErr error
}

func (s *recordingThreadInterruptSignaler) SignalWorkflow(_ context.Context, workflowID, runID, signalName string, signalArg interface{}) error {
	if s.failErr != nil {
		return s.failErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = append(s.signals, recordedThreadInterruptSignal{
		workflowID: workflowID,
		runID:      runID,
		signalName: signalName,
		signalArg:  signalArg,
	})
	return nil
}

func (s *recordingThreadInterruptSignaler) recorded() []recordedThreadInterruptSignal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedThreadInterruptSignal, len(s.signals))
	copy(out, s.signals)
	return out
}

type recordingThreadInterruptCanceler struct {
	mu        sync.Mutex
	cancelled []string
	failFor   map[string]bool
}

func newRecordingThreadInterruptCanceler() *recordingThreadInterruptCanceler {
	return &recordingThreadInterruptCanceler{failFor: map[string]bool{}}
}

func (c *recordingThreadInterruptCanceler) SendToolExecutionCancel(_ context.Context, _, requestID, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failFor[requestID] {
		return errors.New("daemon offline")
	}
	c.cancelled = append(c.cancelled, requestID)
	return nil
}

func (c *recordingThreadInterruptCanceler) cancelledIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.cancelled))
	copy(out, c.cancelled)
	return out
}

func newInterruptFixture(t *testing.T, repo *db.Repo, userID string) interruptFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	projectID := "test-project-interrupt-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Interrupt Test",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	chatID := uuid.NewString()
	rootThreadID := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, Title: "Test Chat", ProjectID: projectID,
		State: db.ChatStateIdle, WorkflowID: &rootThreadID,
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: rootThreadID, ChatID: chatID, WorkflowID: &rootThreadID,
		Origin: db.ThreadOriginMain, Status: db.ThreadStatusRunning, CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID: uuid.NewString(), ThreadID: rootThreadID, Sequence: 0, CreatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: rootThreadID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: rootThreadID, Status: db.Active(), CreatedAt: now,
	}))

	childThreadID := uuid.NewString()
	_, err = repo.CreateThread(ctx, &db.Thread{
		ID: childThreadID, ChatID: chatID, ParentThreadID: &rootThreadID,
		WorkflowID: &childThreadID, Origin: db.ThreadOriginSpawn,
		Status: db.ThreadStatusRunning, CreatedAt: now,
	})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{
		ID: uuid.NewString(), ThreadID: childThreadID, Sequence: 0, CreatedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: childThreadID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: childThreadID, ParentID: &rootThreadID,
		Status: db.Active(), CreatedAt: now,
	}))

	return interruptFixture{
		userID:        userID,
		chatID:        chatID,
		rootThreadID:  rootThreadID,
		childThreadID: childThreadID,
	}
}

func insertInterruptToolCall(t *testing.T, repo *db.Repo, id, chatID, threadID, toolName string, status core.ToolCallStatus) {
	t.Helper()
	now := time.Now().UTC()
	var completedAt *time.Time
	if status == core.ToolCallStatusCompleted {
		completedAt = &now
	}
	require.NoError(t, repo.UpsertToolCall(context.Background(), &db.ToolCall{
		ID:          id,
		ChatID:      chatID,
		ThreadID:    &threadID,
		ToolName:    toolName,
		Status:      status,
		RequestedAt: now,
		StartedAt:   &now,
		CompletedAt: completedAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}))
}

func TestInterruptThread_CancelsExecutingToolCallsOnThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	signaler := &recordingThreadInterruptSignaler{}
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(signaler), WithToolCanceler(canceler))

	bashCall := "toolu_" + uuid.NewString()
	viewCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, bashCall, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, viewCall, fx.chatID, fx.rootThreadID, "view", core.ToolCallStatusExecuting)

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	assert.Equal(t, 2, result.CancelledToolCalls)
	assert.Empty(t, result.UndeliverableToolCalls)
	assert.ElementsMatch(t, []string{bashCall, viewCall}, canceler.cancelledIDs(),
		"every executing tool on the thread must be asked to stop")

	signals := signaler.recorded()
	require.Len(t, signals, 1)
	assert.Equal(t, fx.rootThreadID, signals[0].workflowID)
	assert.Equal(t, v2.InterruptThreadSignalName, signals[0].signalName)
	signal, ok := signals[0].signalArg.(v2.InterruptThreadSignal)
	require.True(t, ok)
	assert.Equal(t, fx.rootThreadID, signal.ThreadID)
}

func TestInterruptThread_LeavesOtherThreadsAlone(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	rootCall := "toolu_" + uuid.NewString()
	spawnCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, rootCall, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, spawnCall, fx.chatID, fx.childThreadID, "bash", core.ToolCallStatusExecuting)

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, result.CancelledToolCalls)
	assert.Equal(t, []string{rootCall}, canceler.cancelledIDs(),
		"interrupting the root thread must not cancel a spawned agent's work; "+
			"a sub-agent on another thread keeps working")
}

func TestInterruptThread_IgnoresAlreadyFinishedToolCalls(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	doneCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, doneCall, fx.chatID, fx.rootThreadID, "view", core.ToolCallStatusCompleted)

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, result.CancelledToolCalls)
	assert.Empty(t, canceler.cancelledIDs(),
		"a completed tool call must not be cancelled by an interrupt")
}

// A tool call the agent has committed to but that has not started yet must be
// cancelled too.
//
// Chat b7cd65c6 (2026-08-16) is the case: a spawn_status(wait:true) sat at
// PENDING from 22:37:35 to 22:41:00 while the user fired six interrupts. Every
// one skipped it -- each logged cancelledToolCalls=0 -- because only EXECUTING
// was considered. The call then started, blocked for 8m44s, and the turn never
// came back around to call_llm, which is the only place the mailbox drains. Five
// queued messages sat undelivered for nine minutes.
//
// PENDING means "recorded, about to dispatch" (execute_tools writes it
// immediately before handing the call to the executor). Between that write and
// the EXECUTING write there is a real window, and an interrupt landing inside it
// has to count -- otherwise the user's stop silently does nothing.
func TestInterruptThread_CancelsPendingToolCallsOnThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	pendingCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, pendingCall, fx.chatID, fx.rootThreadID, "spawn_status", core.ToolCallStatusPending)

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, result.CancelledToolCalls,
		"a pending tool call is in flight as far as the user is concerned")
	assert.Equal(t, []string{pendingCall}, canceler.cancelledIDs())
}

func TestInterruptThread_NothingExecutingSucceeds(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	signaler := &recordingThreadInterruptSignaler{}
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(signaler), WithToolCanceler(canceler))

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, result.CancelledToolCalls)
	assert.Empty(t, result.UndeliverableToolCalls)
	assert.Empty(t, canceler.cancelledIDs())
	require.Len(t, signaler.recorded(), 1,
		"an interrupt with no executing tools still wakes the workflow to read the mailbox")
}

func TestInterruptThread_ReportsUndeliverableCancels(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	canceler := newRecordingThreadInterruptCanceler()
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}), WithToolCanceler(canceler))

	reachable := "toolu_" + uuid.NewString()
	unreachable := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, reachable, fx.chatID, fx.rootThreadID, "view", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, unreachable, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)
	canceler.failFor[unreachable] = true

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	assert.Equal(t, 1, result.CancelledToolCalls)
	assert.Equal(t, []string{unreachable}, result.UndeliverableToolCalls,
		"a cancel that could not be delivered must be named, not counted as cancelled")
}

func TestInterruptThread_LeavesQueuedMailboxForDelivery(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo,
		WithTemporalSignaler(&recordingThreadInterruptSignaler{}),
		WithToolCanceler(newRecordingThreadInterruptCanceler()),
	)

	queuedBody := "actually, stop and check the migration first"
	require.NoError(t, repo.EnqueueAgentMessage(ctx, &db.AgentMessage{
		ID:           uuid.NewString(),
		ChatID:       fx.chatID,
		FromThreadID: fx.rootThreadID,
		ToThreadID:   fx.rootThreadID,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         queuedBody,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    time.Now().UTC(),
	}))

	toolCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, toolCall, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.CancelledToolCalls)

	queued, err := repo.ListQueuedAgentMessagesForThread(ctx, fx.rootThreadID)
	require.NoError(t, err)
	require.Len(t, queued, 1,
		"interrupt must leave the mailbox for call_llm to deliver, not consume it itself")
	assert.Equal(t, queuedBody, queued[0].Body)
}

func TestInterruptThread_RejectsWrongOwnerAndCrossChat(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "owner-user")
	otherFx := newInterruptFixture(t, repo, "other-user")
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{}))

	_, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: "other-user", ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: otherFx.rootThreadID,
	})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestInterruptThread_RequiresChatThreadAndUser(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo)

	_, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID:   fx.userID,
		ThreadID: fx.rootThreadID,
	})
	require.ErrorIs(t, err, ErrInvalidArgument)

	_, err = svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID,
		ChatID: fx.chatID,
	})
	require.ErrorIs(t, err, ErrInvalidArgument)

	_, err = svc.InterruptThread(ctx, InterruptThreadOpts{
		ChatID:   fx.chatID,
		ThreadID: fx.rootThreadID,
	})
	require.ErrorIs(t, err, ErrInvalidArgument)
}

// orderRecordingCanceler and orderRecordingSignaler write into one shared,
// mutex-guarded log so a test can assert the RELATIVE order of the daemon
// cancel push and the workflow signal -- not just that both happened.
type orderRecordingCanceler struct {
	mu  *sync.Mutex
	log *[]string
}

func (c orderRecordingCanceler) SendToolExecutionCancel(_ context.Context, _, requestID, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.log = append(*c.log, "cancel:"+requestID)
	return nil
}

type orderRecordingSignaler struct {
	mu  *sync.Mutex
	log *[]string
}

func (s orderRecordingSignaler) SignalWorkflow(_ context.Context, _, _, _ string, _ interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.log = append(*s.log, "signal")
	return nil
}

// TestInterruptThread_CancelsToolsBeforeSignalingWorkflow pins the fast-cancel
// ordering fix. Signalling the workflow frees it to re-dispatch the step, so
// if that happened before the daemon cancel push landed, the successor step
// could begin while the predecessor's tool is still provably running --
// tools are not idempotent, so a re-entered call is a correctness bug. This
// test fails if the signal is ever moved back in front of the cancel push.
func TestInterruptThread_CancelsToolsBeforeSignalingWorkflow(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")

	var mu sync.Mutex
	var order []string
	svc := NewService(repo,
		WithTemporalSignaler(orderRecordingSignaler{mu: &mu, log: &order}),
		WithToolCanceler(orderRecordingCanceler{mu: &mu, log: &order}),
	)

	callID := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, callID, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)

	result, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.CancelledToolCalls)

	require.Equal(t, []string{"cancel:" + callID, "signal"}, order,
		"the daemon cancel must be pushed BEFORE the workflow is signalled -- "+
			"signalling first frees the workflow to re-dispatch the step while "+
			"the predecessor tool is still alive")
}

func TestInterruptThread_ReportsUndeliverableWorkflowSignal(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo, WithTemporalSignaler(&recordingThreadInterruptSignaler{
		failErr: errors.New("temporal unavailable"),
	}))

	_, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.ErrorIs(t, err, ErrInterruptUndeliverable)
}

// THE CANCELLATION MUST BE DURABLE THE MOMENT IT IS ASKED FOR.
//
// Interrupt is immediate; the resume that would let the activity notice it
// stopped is not, and under pause may never come. If the durable row is only
// written when the activity unwinds, the user watches a tool spin long after
// they stopped it, and a reload still shows it running.
//
// It is also what closes the re-entry hole: the row becomes TERMINAL, which is
// what checkPriorTerminalResult looks for, so a re-dispatch of the same
// tool_call_id returns the recorded cancellation instead of running the tool
// again (an interrupted spawn_status(wait) otherwise restarts its whole wait).
func TestInterruptThread_RecordsCancellationDurablyImmediately(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo,
		WithTemporalSignaler(&recordingThreadInterruptSignaler{}),
		WithToolCanceler(newRecordingThreadInterruptCanceler()))

	callID := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, callID, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)

	_, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	call, err := repo.GetToolCall(ctx, callID)
	require.NoError(t, err)
	require.NotNil(t, call)
	assert.Equal(t, core.ToolCallStatusCancelled, call.Status,
		"the cancelled call must be terminal in the DB as soon as the user asks, not whenever the activity next runs")
	assert.True(t, call.Status.IsTerminal(),
		"terminal is what checkPriorTerminalResult keys on; a non-terminal row lets the tool re-run")
	assert.NotNil(t, call.CompletedAt, "a terminal call must record when it stopped")

	result, err := repo.GetToolCallResult(ctx, callID)
	require.NoError(t, err)
	require.NotNil(t, result, "a terminal row with no result leaves the transcript with a dangling tool_call")
	assert.True(t, result.IsError, "a cancelled tool did not succeed")
	assert.Equal(t, CancelledToolResultContent, result.Content)
}

// A tool call that already FINISHED keeps its real outcome. Interrupt cancels
// what is in flight; it must not rewrite history for work the user already has.
func TestInterruptThread_DoesNotRewriteAlreadyFinishedCalls(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo,
		WithTemporalSignaler(&recordingThreadInterruptSignaler{}),
		WithToolCanceler(newRecordingThreadInterruptCanceler()))

	doneID := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, doneID, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusCompleted)

	_, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	call, err := repo.GetToolCall(ctx, doneID)
	require.NoError(t, err)
	require.NotNil(t, call)
	assert.Equal(t, core.ToolCallStatusCompleted, call.Status,
		"a call that already completed must keep its real outcome")
}

// Scoping: interrupting one thread must not cancel another thread's work.
// A spawned sub-agent runs alongside the root, and stopping the root is not
// a request to stop the child.
func TestInterruptThread_DurableCancelIsScopedToTheThread(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()
	fx := newInterruptFixture(t, repo, "test-user")
	svc := NewService(repo,
		WithTemporalSignaler(&recordingThreadInterruptSignaler{}),
		WithToolCanceler(newRecordingThreadInterruptCanceler()))

	rootCall := "toolu_" + uuid.NewString()
	childCall := "toolu_" + uuid.NewString()
	insertInterruptToolCall(t, repo, rootCall, fx.chatID, fx.rootThreadID, "bash", core.ToolCallStatusExecuting)
	insertInterruptToolCall(t, repo, childCall, fx.chatID, fx.childThreadID, "bash", core.ToolCallStatusExecuting)

	_, err := svc.InterruptThread(ctx, InterruptThreadOpts{
		UserID: fx.userID, ChatID: fx.chatID, ThreadID: fx.rootThreadID,
	})
	require.NoError(t, err)

	child, err := repo.GetToolCall(ctx, childCall)
	require.NoError(t, err)
	require.NotNil(t, child)
	assert.Equal(t, core.ToolCallStatusExecuting, child.Status,
		"another thread's in-flight tool must be untouched")
}
