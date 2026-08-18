// Copyright (c) 2025 Reliant Labs
package runs

import (
	"context"
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

type fakeRepo struct {
	chat        *db.Chat
	chatErr     error
	workflow    *db.Workflow
	workflowErr error

	updatedChats       []db.Chat
	updatedStatuses    map[string]db.WorkflowStatus
	deletedCheckpoints []string
	casCalls           []workflowCASCall
	casErr             error
	cascadedReason     db.WorkflowStopReason
	cascadedThread     db.WorkflowStopReason
	pendingQuestion    *db.Question
	questionErr        error
	resolvedQuestionID string
	resolvedResponse   *string
	emittedQuestions   []db.QuestionUpdate
}

type workflowCASCall struct {
	ID             string
	NewStatus      db.WorkflowStatus
	ExpectedStatus db.WorkflowStatus
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{updatedStatuses: map[string]db.WorkflowStatus{}}
}

func (f *fakeRepo) GetChat(context.Context, string) (*db.Chat, error) {
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	c := *f.chat
	return &c, nil
}

func (f *fakeRepo) UpdateChat(_ context.Context, chat *db.Chat) error {
	f.updatedChats = append(f.updatedChats, *chat)
	f.chat = chat
	return nil
}

func (f *fakeRepo) GetWorkflow(context.Context, string) (*db.Workflow, error) {
	return f.workflow, f.workflowErr
}

func (f *fakeRepo) UpdateWorkflowStatus(_ context.Context, id string, status db.WorkflowStatus) error {
	f.updatedStatuses[id] = status
	return nil
}

func (f *fakeRepo) DeleteWorkflowCheckpoint(_ context.Context, workflowID string) error {
	f.deletedCheckpoints = append(f.deletedCheckpoints, workflowID)
	return nil
}

func (f *fakeRepo) CompareAndSwapWorkflowStatus(_ context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error) {
	f.casCalls = append(f.casCalls, workflowCASCall{ID: id, NewStatus: newStatus, ExpectedStatus: expectedStatus})
	if f.casErr != nil {
		return false, f.casErr
	}
	if f.workflow != nil && f.workflow.ID == id && f.workflow.Status == expectedStatus {
		f.workflow.Status = newStatus
		f.updatedStatuses[id] = newStatus
		return true, nil
	}
	return false, nil
}

func (f *fakeRepo) CascadeTerminalStatusToDescendants(_ context.Context, _ string, reason db.WorkflowStopReason) error {
	f.cascadedReason = reason
	return nil
}

func (f *fakeRepo) CascadeTerminalStatusToThreadSubtree(_ context.Context, _ string, reason db.WorkflowStopReason) error {
	f.cascadedThread = reason
	return nil
}

func (f *fakeRepo) GetPendingQuestionByChatID(context.Context, string) (*db.Question, error) {
	return f.pendingQuestion, f.questionErr
}

func (f *fakeRepo) ResolveQuestion(_ context.Context, id string, responseData *string) error {
	f.resolvedQuestionID = id
	f.resolvedResponse = responseData
	if f.pendingQuestion != nil && f.pendingQuestion.ID == id {
		f.pendingQuestion.Status = db.QuestionStatusResolved
		f.pendingQuestion.ResponseData = responseData
	}
	return nil
}

func (f *fakeRepo) EmitQuestionUpdate(_ context.Context, _ string, update db.QuestionUpdate) error {
	f.emittedQuestions = append(f.emittedQuestions, update)
	return nil
}

type fakeTemporal struct {
	status enumspb.WorkflowExecutionStatus
	runID  string
	// runIDs, when non-empty, is consumed one entry per Describe call, so a
	// test can model a reset that mints a new run between two reads.
	runIDs             []string
	notFound           bool
	err                error
	calls              int
	terminated         []string
	terminationReasons []string
	terminateErr       error
}

func (f *fakeTemporal) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.notFound {
		return nil, errors.New("workflow not found")
	}
	runID := f.runID
	if len(f.runIDs) > 0 {
		runID = f.runIDs[0]
		if len(f.runIDs) > 1 {
			f.runIDs = f.runIDs[1:]
		}
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: "wf-1", RunId: runID},
			Status:    f.status,
		},
	}, nil
}

func (f *fakeTemporal) TerminateWorkflow(_ context.Context, workflowID, _, reason string, _ ...interface{}) error {
	f.terminated = append(f.terminated, workflowID)
	f.terminationReasons = append(f.terminationReasons, reason)
	return f.terminateErr
}

type fakePause struct {
	pauseErr            error
	resumeErr           error
	interruptedRunID    string
	interruptedErr      error
	signalErr           error
	pauseCalls          []string
	resumeCalls         []string
	interruptedCalls    []string
	signalCalls         []string
	signalTargetWfIDs   []string
	resumeWorkflowIDArg string
}

func (f *fakePause) PauseWorkflow(_ context.Context, workflowID, _, _ string) error {
	f.pauseCalls = append(f.pauseCalls, workflowID)
	return f.pauseErr
}

func (f *fakePause) ResumeWorkflow(_ context.Context, workflowID, _ string) error {
	f.resumeCalls = append(f.resumeCalls, workflowID)
	f.resumeWorkflowIDArg = workflowID
	return f.resumeErr
}

func (f *fakePause) ResumeInterruptedWorkflow(_ context.Context, workflowID, _ string) (string, error) {
	f.interruptedCalls = append(f.interruptedCalls, workflowID)
	return f.interruptedRunID, f.interruptedErr
}

func (f *fakePause) SignalWithRecovery(_ context.Context, workflowID, signalName string, _ interface{}) error {
	f.signalCalls = append(f.signalCalls, signalName)
	f.signalTargetWfIDs = append(f.signalTargetWfIDs, workflowID)
	return f.signalErr
}

func strPtr(s string) *string { return &s }

// fixture builds a service over a chat that has a root workflow.
func fixture(t *testing.T) (*fakeRepo, *fakeTemporal, *fakePause, *Service) {
	t.Helper()
	repo := newFakeRepo()
	repo.chat = &db.Chat{ID: "chat-1", WorkflowID: strPtr("wf-1"), RunID: strPtr("run-old")}
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Paused()}
	temporal := &fakeTemporal{status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, runID: "run-old"}
	pause := &fakePause{}
	return repo, temporal, pause, NewService(repo, temporal, pause)
}

// =========================================================================
// Terminate — hard stop policy owned by runs
// =========================================================================

func TestTerminate_LiveRunHardTerminatesAndDrainsSubtree(t *testing.T) {
	repo, temporal, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	repo.pendingQuestion = &db.Question{ID: "q-1", ChatID: "chat-1", WorkflowID: "wf-1", ThreadID: "chat-1", StepID: "ask"}

	err := svc.Terminate(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, []string{"wf-1"}, temporal.terminated)
	assert.Equal(t, []string{"Workflow terminated by operator"}, temporal.terminationReasons)
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints,
		"operator terminate must drop the checkpoint so the next message starts fresh")
	require.Len(t, repo.casCalls, 1)
	assert.Equal(t, db.Cancelled(), repo.casCalls[0].NewStatus)
	assert.Equal(t, db.Active(), repo.casCalls[0].ExpectedStatus)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedThread)
	assert.Equal(t, "q-1", repo.resolvedQuestionID)
	require.NotNil(t, repo.resolvedResponse)
	assert.Contains(t, *repo.resolvedResponse, "terminated by operator")
	require.Len(t, repo.emittedQuestions, 1)
	assert.Equal(t, "resolved", repo.emittedQuestions[0].Status)
}

func TestTerminate_PausedRunUsesPausedCAS(t *testing.T) {
	repo, _, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Paused()}

	err := svc.Terminate(context.Background(), "chat-1")

	require.NoError(t, err)
	require.Len(t, repo.casCalls, 1)
	assert.Equal(t, db.Paused(), repo.casCalls[0].ExpectedStatus)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"])
}

func TestTerminate_NotRunningReconcilesTemporalStatus(t *testing.T) {
	repo, temporal, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED

	err := svc.Terminate(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Empty(t, temporal.terminated,
		"a closed Temporal execution should be reconciled, not terminated again")
	assert.Equal(t, []string{"wf-1"}, repo.deletedCheckpoints)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedThread)
}

func TestTerminate_ClosedFailedTemporalStillStartsFresh(t *testing.T) {
	repo, temporal, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_FAILED

	err := svc.Terminate(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Empty(t, temporal.terminated)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"],
		"operator terminate must not leave a resumable failed status after dropping the checkpoint")
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedThread)
}

func TestTerminate_TemporalNotFoundStillMarksCancelled(t *testing.T) {
	repo, temporal, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	temporal.notFound = true

	err := svc.Terminate(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Empty(t, temporal.terminated)
	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason)
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedThread)
}

func TestTerminate_CASErrorSurfaces(t *testing.T) {
	repo, _, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Active()}
	repo.casErr = errors.New("db unavailable")

	err := svc.Terminate(context.Background(), "chat-1")

	require.Error(t, err)
}

// =========================================================================
// The stuck check — the decision this service exists to own exactly once
// =========================================================================

// A run the database calls failed while Temporal still reports it RUNNING is
// stuck: nothing can reconcile that, so it must never be signalled. The caller
// renders "use branch" from Unresumable, and — the part that matters — the
// resume is not attempted at all.
func TestResume_StuckRunIsRefusedWithoutSignalling(t *testing.T) {
	repo, temporal, pause, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Failed()}
	temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeUnresumable, outcome.Kind)
	assert.Empty(t, pause.resumeCalls,
		"a stuck run must never be signalled — that is the whole point of the check")
}

// Failed in the database AND closed in Temporal is the ordinary interrupted
// run, not a stuck one. Only the disagreement makes it stuck.
func TestResume_FailedButClosedInTemporalIsNotStuck(t *testing.T) {
	repo, temporal, pause, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Failed()}
	temporal.status = enumspb.WORKFLOW_EXECUTION_STATUS_FAILED

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeResumed, outcome.Kind)
	assert.Equal(t, []string{"wf-1"}, pause.resumeCalls)
}

// A Temporal query that fails must not be read as "stuck". Refusing to resume
// because we could not reach Temporal would strand a perfectly resumable chat
// behind an outage.
func TestResume_TemporalQueryFailureIsNotTreatedAsStuck(t *testing.T) {
	repo, temporal, pause, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Failed()}
	temporal.err = errors.New("connection refused")

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeResumed, outcome.Kind)
	assert.Equal(t, []string{"wf-1"}, pause.resumeCalls,
		"an unreachable Temporal must not block a resume")
}

// =========================================================================
// Run-id refresh — a reset mints a NEW run, so the stored id goes stale
// =========================================================================

// The reason the refresh is unconditional: a resume may have RESET the
// execution, and the chat's stored run id is stale exactly then. The new id
// must be both returned and written back.
func TestResume_RefreshesRunIDAfterReset(t *testing.T) {
	repo, temporal, _, svc := fixture(t)
	temporal.runID = "run-new-after-reset"

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeResumed, outcome.Kind)
	assert.Equal(t, "run-new-after-reset", outcome.RunID)
	require.Len(t, repo.updatedChats, 1, "the new run id must be persisted")
	assert.Equal(t, "run-new-after-reset", *repo.updatedChats[0].RunID)
}

// An unchanged run id costs no write.
func TestResume_UnchangedRunIDIsNotRewritten(t *testing.T) {
	repo, _, _, svc := fixture(t)

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, "run-old", outcome.RunID)
	assert.Empty(t, repo.updatedChats, "an unchanged run id must not trigger a write")
}

// If the follow-up read does not land, a resume that genuinely succeeded must
// still be reported as success, falling back to the stored id.
func TestResume_FallsBackToStoredRunIDWhenTemporalUnreadable(t *testing.T) {
	repo, temporal, _, svc := fixture(t)
	repo.workflow = &db.Workflow{ID: "wf-1", Status: db.Paused()}
	temporal.notFound = true

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeResumed, outcome.Kind)
	assert.Equal(t, "run-old", outcome.RunID)
}

// =========================================================================
// Error -> outcome mapping
// =========================================================================

// A workflow Temporal no longer has is not an error to the user: the messages
// survive, so the caller prompts to start a new conversation.
func TestResume_LostWorkflowMapsToNeedsRecovery(t *testing.T) {
	_, _, pause, svc := fixture(t)
	pause.resumeErr = workflow.ErrWorkflowNotFound

	outcome, err := svc.Resume(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeNeedsRecovery, outcome.Kind)
	assert.Equal(t, "wf-1", outcome.WorkflowID)
}

// Any other resume failure is a real error — the caller must not render
// "resumed" over a run that never woke.
func TestResume_UnexpectedErrorSurfaces(t *testing.T) {
	_, _, pause, svc := fixture(t)
	pause.resumeErr = errors.New("temporal unavailable")

	_, err := svc.Resume(context.Background(), "chat-1")

	require.Error(t, err)
}

func TestResumeInterrupted_MapsSentinelsToOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantKind      OutcomeKind
		wantHistLimit bool
	}{
		{"reset-and-replay served it", nil, OutcomeResumed, false},
		{"nothing to replay", workflow.ErrNoReplayableHistory, OutcomeNeedsRestart, false},
		{"guard gave up", workflow.ErrResetAttemptsExhausted, OutcomeNeedsRestart, false},
		{"at the history cap", workflow.ErrHistoryLimitExceeded, OutcomeNeedsRestart, true},
		{"unexpected failure still falls back", errors.New("boom"), OutcomeNeedsRestart, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, pause, svc := fixture(t)
			pause.interruptedErr = tt.err
			pause.interruptedRunID = "run-reset"

			outcome, err := svc.ResumeInterrupted(context.Background(), "chat-1")

			require.NoError(t, err, "a fallback is a normal result, never an error")
			assert.Equal(t, tt.wantKind, outcome.Kind)
			assert.Equal(t, tt.wantHistLimit, outcome.HistoryLimitExceeded,
				"only the history-cap case may ask the caller to tell the user")
		})
	}
}

// The history cap is singled out because the caller must TELL the user: a reset
// forks from inside the oversized history and dies within a few events, so
// without an explanation the chat just stops responding.
func TestResumeInterrupted_HistoryLimitIsDistinguishableFromOtherFallbacks(t *testing.T) {
	_, _, pause, svc := fixture(t)
	pause.interruptedErr = workflow.ErrNoReplayableHistory

	outcome, err := svc.ResumeInterrupted(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeNeedsRestart, outcome.Kind)
	assert.False(t, outcome.HistoryLimitExceeded,
		"an ordinary fallback must not trigger the user-facing history-limit notice")
}

func TestResumeInterrupted_SuccessRecordsNewRunID(t *testing.T) {
	repo, _, pause, svc := fixture(t)
	pause.interruptedRunID = "run-reset"

	outcome, err := svc.ResumeInterrupted(context.Background(), "chat-1")

	require.NoError(t, err)
	assert.Equal(t, OutcomeResumed, outcome.Kind)
	assert.Equal(t, "run-reset", outcome.RunID)
	require.NotEmpty(t, repo.updatedChats)
	assert.Equal(t, "run-reset", *repo.updatedChats[0].RunID)
}

// =========================================================================
// Inspect
// =========================================================================

func TestInspect(t *testing.T) {
	tests := []struct {
		name            string
		dbStatus        db.WorkflowStatus
		temporalStatus  enumspb.WorkflowExecutionStatus
		notFound        bool
		wantStuck       bool
		wantRecoverable bool
	}{
		{"failed row + open execution is stuck", db.Failed(), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, false, true, false},
		{"active row + open execution is neither", db.Active(), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, false, false, false},
		{"closed execution has history to replay", db.Failed(), enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, false, false, true},
		{"terminated execution has history to replay", db.Failed(), enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, false, false, true},
		{"ghost has nothing to replay", db.Failed(), enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, temporal, _, svc := fixture(t)
			repo.workflow = &db.Workflow{ID: "wf-1", Status: tt.dbStatus}
			temporal.status = tt.temporalStatus
			temporal.notFound = tt.notFound

			got, err := svc.Inspect(context.Background(), "chat-1")

			require.NoError(t, err)
			assert.Equal(t, tt.wantStuck, got.Stuck)
			assert.Equal(t, tt.wantRecoverable, got.Recoverable)
		})
	}
}

// =========================================================================
// State — the Temporal -> our-status mapping
// =========================================================================

// TERMINATED maps to Failed at raw Temporal-state inspection time because
// Temporal does not carry the caller's intent. The explicit operator terminate
// path writes Cancelled itself after dropping the checkpoint; reconciler/system
// terminates keep Failed so the next message can resume at position.
func TestState_MapsTemporalStatuses(t *testing.T) {
	tests := []struct {
		temporal      enumspb.WorkflowExecutionStatus
		want          db.WorkflowStatus
		wantIsRunning bool
	}{
		{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, db.Active(), true},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, db.Active(), true},
		{enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, db.Completed(), false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, db.Failed(), false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, db.Failed(), false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, db.Failed(), false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, db.Cancelled(), false},
	}

	for _, tt := range tests {
		t.Run(tt.temporal.String(), func(t *testing.T) {
			_, temporal, _, svc := fixture(t)
			temporal.status = tt.temporal

			got, err := svc.State(context.Background(), "wf-1")

			require.NoError(t, err)
			assert.True(t, got.Exists)
			assert.Equal(t, tt.want, got.Status)
			assert.Equal(t, tt.wantIsRunning, got.IsRunning)
		})
	}
}

// A missing execution is an expected state with its own recovery path, not an
// error the caller has to special-case.
func TestState_NotFoundIsNotAnError(t *testing.T) {
	_, temporal, _, svc := fixture(t)
	temporal.notFound = true

	got, err := svc.State(context.Background(), "wf-1")

	require.NoError(t, err)
	assert.False(t, got.Exists)
}

// A genuine outage IS an error — reporting it as "no such execution" would send
// a live chat down the ghost-recovery path and restart a running workflow.
func TestState_GenuineErrorSurfaces(t *testing.T) {
	_, temporal, _, svc := fixture(t)
	temporal.err = errors.New("connection refused")

	_, err := svc.State(context.Background(), "wf-1")

	require.Error(t, err)
}

// =========================================================================
// Pause, ResumeViaSignal, and lookup failures
// =========================================================================

func TestPause_DelegatesWithChatWorkflowID(t *testing.T) {
	_, _, pause, svc := fixture(t)

	require.NoError(t, svc.Pause(context.Background(), "chat-1"))
	assert.Equal(t, []string{"wf-1"}, pause.pauseCalls)
}

// A run parked on an unanswered question wakes on the question's own channel,
// not signal.resume — and that channel lives on the SUB-workflow that owns the
// question, which is not the chat's root workflow.
func TestResumeViaSignal_AddressesTheSignalToItsOwnWorkflow(t *testing.T) {
	repo, temporal, pause, svc := fixture(t)
	temporal.runID = "run-after-question-reset"

	outcome, err := svc.ResumeViaSignal(context.Background(), ResumeViaSignalInput{
		ChatID:           "chat-1",
		WorkflowID:       "wf-1",
		TargetWorkflowID: "wf-nested-child",
		SignalName:       "signal.question.q-7",
	})

	require.NoError(t, err)
	assert.Equal(t, OutcomeResumed, outcome.Kind)
	assert.Equal(t, []string{"wf-nested-child"}, pause.signalTargetWfIDs,
		"the signal must go to the workflow that owns the question channel")
	assert.Equal(t, []string{"signal.question.q-7"}, pause.signalCalls)
	assert.Equal(t, db.Active(), repo.updatedStatuses["wf-1"],
		"the chat's ROOT workflow is the row marked running")
	assert.Equal(t, "run-after-question-reset", outcome.RunID)
}

// Undeliverable is a fallback, not a failure: the caller coarse-restarts.
func TestResumeViaSignal_UndeliverableFallsBackToRestart(t *testing.T) {
	repo, _, pause, svc := fixture(t)
	pause.signalErr = errors.New("guard exhausted")

	outcome, err := svc.ResumeViaSignal(context.Background(), ResumeViaSignalInput{
		ChatID: "chat-1", WorkflowID: "wf-1",
		TargetWorkflowID: "wf-1", SignalName: "signal.question.q-7",
	})

	require.NoError(t, err)
	assert.Equal(t, OutcomeNeedsRestart, outcome.Kind)
	assert.Empty(t, repo.updatedStatuses,
		"a run that was never woken must not be marked running")
}

func TestLifecycleCalls_RejectChatWithoutWorkflow(t *testing.T) {
	repo, temporal, pause, _ := fixture(t)
	repo.chat = &db.Chat{ID: "chat-1"} // never started a root workflow
	svc := NewService(repo, temporal, pause)

	_, resumeErr := svc.Resume(context.Background(), "chat-1")
	assert.ErrorIs(t, resumeErr, ErrNoWorkflow)
	assert.ErrorIs(t, svc.Pause(context.Background(), "chat-1"), ErrNoWorkflow)
	assert.ErrorIs(t, svc.Terminate(context.Background(), "chat-1"), ErrNoWorkflow)
}

func TestLifecycleCalls_RejectMissingChat(t *testing.T) {
	repo, temporal, pause, _ := fixture(t)
	repo.chatErr = errors.New("no rows")
	svc := NewService(repo, temporal, pause)

	_, err := svc.Resume(context.Background(), "chat-1")
	assert.ErrorIs(t, err, ErrChatNotFound)
	assert.ErrorIs(t, svc.Terminate(context.Background(), "chat-1"), ErrChatNotFound)
}

// =========================================================================
// Reconcile
// =========================================================================

// A terminal repair must drain the subtree with it: we are here because the run
// ended without its own completion handler writing this status, so that
// handler's cascade did not happen either and every spawn/thread row is still
// at running or paused.
func TestReconcile_TerminalRepairCascadesWithTheRealReason(t *testing.T) {
	repo, _, _, svc := fixture(t)

	svc.Reconcile(context.Background(), "wf-1", db.Active(), db.Cancelled())

	assert.Equal(t, db.Cancelled(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedReason,
		"a repaired cancel must not read as a repaired success")
	assert.Equal(t, db.StopReasonCancelled, repo.cascadedThread)
}

// A run that stopped only because it is PAUSED has not ended, and draining its
// subtree would kill work that is coming back.
func TestReconcile_PausedDoesNotCascade(t *testing.T) {
	repo, _, _, svc := fixture(t)

	svc.Reconcile(context.Background(), "wf-1", db.Active(), db.Paused())

	assert.Equal(t, db.Paused(), repo.updatedStatuses["wf-1"])
	assert.Equal(t, db.WorkflowStopReason(0), repo.cascadedReason,
		"a paused run's subtree must be left alone")
}

func TestReconcile_NoOpWhenStatusesAgree(t *testing.T) {
	repo, _, _, svc := fixture(t)

	svc.Reconcile(context.Background(), "wf-1", db.Active(), db.Active())

	assert.Empty(t, repo.updatedStatuses)
}
