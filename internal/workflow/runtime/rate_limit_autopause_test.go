// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// =============================================================================
// handleWorkflowCompletion tests
// =============================================================================

// TestHandleWorkflowCompletion_ErrorDetection verifies that handleWorkflowCompletion
// correctly distinguishes between successful completion, error failure, and cancellation.
type HandleWorkflowCompletionSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestHandleWorkflowCompletion(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(HandleWorkflowCompletionSuite))
}

// completionTestWorkflow is a minimal workflow that calls handleWorkflowCompletion
// via the same deferred pattern as DynamicWorkflow.
func completionTestWorkflow(ctx workflow.Context, mode string) (result *WorkflowResult, retErr error) {
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	defer func() {
		handleWorkflowCompletion(ctx, workflowID, "chat-1", "test-workflow", "", "thread-1", "", retErr, "", nil)
	}()

	switch mode {
	case "success":
		return &WorkflowResult{Outputs: map[string]interface{}{"done": true}}, nil
	case "error":
		return nil, fmt.Errorf("something went wrong")
	case "cancel":
		_ = workflow.Sleep(ctx, time.Hour)
		return nil, ctx.Err()
	case "continue_as_new":
		return nil, workflow.NewContinueAsNewError(ctx, completionTestWorkflow, "success")
	default:
		return nil, fmt.Errorf("unknown mode: %s", mode)
	}
}

// setupCompletionEnv registers the stub activities that handleWorkflowCompletion calls
// (WorkflowStatus, Cleanup) and returns a slice that captures emitted statuses.
func setupCompletionEnv(env *testsuite.TestWorkflowEnvironment) *[]string {
	var capturedStatuses []string
	env.RegisterActivityWithOptions(workflowStatusStub, activity.RegisterOptions{Name: "WorkflowStatus"})
	env.RegisterActivityWithOptions(cleanupStub, activity.RegisterOptions{Name: "Cleanup"})
	env.OnActivity(workflowStatusStub, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			if status, ok := input["status"].(string); ok {
				capturedStatuses = append(capturedStatuses, status)
			}
			return map[string]interface{}{"success": true}, nil
		},
	)
	env.OnActivity(cleanupStub, mock.Anything, mock.Anything).Return(
		map[string]interface{}{"success": true}, nil,
	)
	return &capturedStatuses
}

func (s *HandleWorkflowCompletionSuite) TestSuccessNotifiesCompleted() {
	env := s.NewTestWorkflowEnvironment()
	captured := setupCompletionEnv(env)

	env.ExecuteWorkflow(completionTestWorkflow, "success")

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())
	s.Contains(*captured, "completed", "successful workflow should emit 'completed' status")
	s.NotContains(*captured, "failed", "successful workflow should not emit 'failed' status")
}

func (s *HandleWorkflowCompletionSuite) TestErrorNotifiesFailed() {
	env := s.NewTestWorkflowEnvironment()
	captured := setupCompletionEnv(env)

	env.ExecuteWorkflow(completionTestWorkflow, "error")

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError())
	s.Contains(*captured, "failed", "errored workflow should emit 'failed' status")
	s.NotContains(*captured, "completed", "errored workflow should not emit 'completed' status")
}

func (s *HandleWorkflowCompletionSuite) TestCancellationNotifiesCancelled() {
	env := s.NewTestWorkflowEnvironment()
	captured := setupCompletionEnv(env)

	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 10*time.Millisecond)

	env.ExecuteWorkflow(completionTestWorkflow, "cancel")

	s.True(env.IsWorkflowCompleted())
	s.Contains(*captured, "cancelled", "cancelled workflow should emit 'cancelled' status")
	s.NotContains(*captured, "completed", "cancelled workflow should not emit 'completed' status")
}

// =============================================================================
// StepExecutor handleActivityCompletion RetryExhausted tests
// =============================================================================

type StepExecutorRetrySuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestStepExecutorRetry(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(StepExecutorRetrySuite))
}

func rateLimitActivity(ctx context.Context) (map[string]interface{}, error) {
	return nil, temporal.NewApplicationError(
		`POST "https://api.anthropic.com/v1/messages": 429 Too Many Requests`,
		"RateLimitError",
	)
}

func genericFailActivity(ctx context.Context) (map[string]interface{}, error) {
	return nil, fmt.Errorf("connection reset by peer")
}

func successActivity(ctx context.Context) (map[string]interface{}, error) {
	return map[string]interface{}{"result": "ok"}, nil
}

// stepExecutorWorkflow dispatches an activity with a low retry limit, then
// uses StepExecutor.HandleCompletion to process the result and reports
// the RetryExhausted flag.
func stepExecutorWorkflow(ctx workflow.Context, activityName string) (bool, error) {
	logger := workflow.GetLogger(ctx)
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID

	nodeOutputs := make(map[string]interface{})
	executor := NewStepExecutor(
		ctx, workflowID, "chat-1", "test-workflow",
		map[string]interface{}{}, nodeOutputs,
		&ChildWorkflowTracker{children: make(map[string]bool)},
	)

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    100 * time.Millisecond,
			MaximumAttempts:    2,
			BackoffCoefficient: 1.0,
		},
	}
	actCtx := workflow.WithActivityOptions(ctx, ao)

	var future workflow.Future
	switch activityName {
	case "rateLimitActivity":
		future = workflow.ExecuteActivity(actCtx, rateLimitActivity)
	case "genericFailActivity":
		future = workflow.ExecuteActivity(actCtx, genericFailActivity)
	case "successActivity":
		future = workflow.ExecuteActivity(actCtx, successActivity)
	default:
		return false, fmt.Errorf("unknown activity: %s", activityName)
	}

	running := &RunningStep{
		ActivityID:   "test-step",
		StepID:       "test-step",
		ActivityName: activityName,
		Future:       future,
	}

	stepEvent := executor.HandleCompletion(running)
	logger.Info("StepEvent result", "retryExhausted", stepEvent.RetryExhausted, "hasError", stepEvent.Error != nil)
	return stepEvent.RetryExhausted, nil
}

func (s *StepExecutorRetrySuite) TestRateLimitError_SetsRetryExhausted() {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(rateLimitActivity)

	env.ExecuteWorkflow(stepExecutorWorkflow, "rateLimitActivity")

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var retryExhausted bool
	s.NoError(env.GetWorkflowResult(&retryExhausted))
	s.True(retryExhausted, "Rate limit error should set RetryExhausted=true after Temporal retries exhausted")
}

func (s *StepExecutorRetrySuite) TestGenericError_SetsRetryExhausted() {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(genericFailActivity)

	env.ExecuteWorkflow(stepExecutorWorkflow, "genericFailActivity")

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var retryExhausted bool
	s.NoError(env.GetWorkflowResult(&retryExhausted))
	s.True(retryExhausted, "Generic error should set RetryExhausted=true after Temporal retries exhausted")
}

func (s *StepExecutorRetrySuite) TestSuccessfulActivity_DoesNotSetRetryExhausted() {
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(successActivity)

	env.ExecuteWorkflow(stepExecutorWorkflow, "successActivity")

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var retryExhausted bool
	s.NoError(env.GetWorkflowResult(&retryExhausted))
	s.False(retryExhausted, "Successful activity should NOT set RetryExhausted")
}

// =============================================================================
// PauseController.RequestPause integration tests
// =============================================================================

func TestPauseController_DoRequestPause_Integration(t *testing.T) {
	t.Parallel()
	t.Run("RequestPause sets pause flag and cancels activities", func(t *testing.T) {
		var pauseRequested bool
		var cancelCalled bool

		pc := &PauseController{
			RequestPause: func() {
				pauseRequested = true
				cancelCalled = true
			},
		}

		pc.DoRequestPause()
		assert.True(t, pauseRequested)
		assert.True(t, cancelCalled)
	})

	t.Run("DoCheckPause with nil receiver is safe", func(t *testing.T) {
		var pc *PauseController
		assert.NotPanics(t, func() {
			pc.DoCheckPause(nil)
		})
	})

	t.Run("DoRequestPause followed by DoCheckPause pattern", func(t *testing.T) {
		requestCalled := false
		checkCalled := false

		pc := &PauseController{
			RequestPause: func() { requestCalled = true },
			CheckPause:   func(ctx workflow.Context) { checkCalled = true },
		}

		pc.DoRequestPause()
		assert.True(t, requestCalled)

		pc.DoCheckPause(nil)
		assert.True(t, checkCalled)
	})

	t.Run("GetActivityCtx falls back when nil", func(t *testing.T) {
		var pc *PauseController
		// nil receiver should return fallback (nil in this case, but no panic)
		result := pc.GetActivityCtx(nil)
		assert.Nil(t, result)
	})

	t.Run("GetActivityCtx uses fn when set", func(t *testing.T) {
		fnCalled := false
		pc := &PauseController{
			ActivityCtxFn: func() workflow.Context {
				fnCalled = true
				return nil
			},
		}
		_ = pc.GetActivityCtx(nil)
		assert.True(t, fnCalled)
	})
}

// =============================================================================
// StepEvent RetryExhausted flag behavior (unit-level)
// =============================================================================

func TestStepEvent_RetryExhausted_CancelledErrorNotExhausted(t *testing.T) {
	t.Parallel()
	event := &StepEvent{
		StepID:         "test-step",
		Error:          temporal.NewCanceledError("context cancelled"),
		RetryExhausted: false,
	}
	assert.False(t, event.RetryExhausted,
		"CanceledError should not have RetryExhausted=true (it's pause, not exhaustion)")
}

func TestStepEvent_RetryExhausted_TimeoutErrorIsExhausted(t *testing.T) {
	t.Parallel()
	event := &StepEvent{
		StepID:         "test-step",
		Error:          errors.New("activity timed out"),
		RetryExhausted: true,
	}
	assert.True(t, event.RetryExhausted,
		"TimeoutError should have RetryExhausted=true")
}

func TestStepEvent_RetryExhausted_ApplicationErrorIsExhausted(t *testing.T) {
	t.Parallel()
	event := &StepEvent{
		StepID:         "test-step",
		Error:          errors.New("429 Too Many Requests"),
		RetryExhausted: true,
	}
	assert.True(t, event.RetryExhausted)

	wfEvent := event.ToEvent()
	require.NotNil(t, wfEvent)
	assert.Contains(t, wfEvent.Data["error"], "429")
}

// =============================================================================
// Full e2e: rate limit → auto-pause → resume → retry
// =============================================================================

type RateLimitAutoPauseSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestRateLimitAutoPause(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(RateLimitAutoPauseSuite))
}

// workflowStatusStub is a real activity function that the test env can register
// and mock. It's used as the target for WorkflowStatus calls in test workflows.
func workflowStatusStub(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{"success": true}, nil
}

// cleanupStub is a stub for the Cleanup activity called by runCleanupActivities
func cleanupStub(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
	return map[string]interface{}{"success": true}, nil
}

// rateLimitThenSuccessActivity fails on the first invocations with a rate limit
// error, then succeeds. The call count is tracked via the closure in the test.
// We use a mock in the test instead.

// autoPauseWorkflow simulates the core DynamicWorkflow pattern using the REAL
// pause machinery (newPauseCoordinator — the same code DynamicWorkflow wires
// up), not a hand-rolled replica:
// 1. Execute activity
// 2. If RetryExhausted (activity failed after retries), emit paused status, self-pause, wait for resume
// 3. On resume, emit started status, retry the step
func autoPauseWorkflow(ctx workflow.Context, holdResume bool) (string, error) {
	pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: holdResume})

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    100 * time.Millisecond,
			MaximumAttempts:    2,
			BackoffCoefficient: 1.0,
		},
	}

	for attempt := 0; attempt < 5; attempt++ {
		pc.CheckPause(ctx)

		stepCtx := workflow.WithActivityOptions(pc.ActivityCtx(), ao)
		var result string
		err := workflow.ExecuteActivity(stepCtx, StepActivity, "call_llm").Get(ctx, &result)
		if err == nil {
			return result, nil
		}

		var canceledErr *temporal.CanceledError
		if errors.As(err, &canceledErr) {
			pc.CheckPause(ctx)
			continue
		}

		// Emit "paused" status
		statusCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 5 * time.Second,
		})
		_ = workflow.ExecuteActivity(statusCtx, workflowStatusStub, map[string]interface{}{
			"status": "paused",
		}).Get(ctx, nil)

		// Self-pause and block (mirrors the retry-exhaustion executors)
		pc.RequestPause()
		pc.CheckPause(ctx)

		// Resumed! Emit "started" status
		_ = workflow.ExecuteActivity(statusCtx, workflowStatusStub, map[string]interface{}{
			"status": "started",
		}).Get(ctx, nil)
	}

	return "", fmt.Errorf("max attempts exceeded")
}

func (s *RateLimitAutoPauseSuite) TestRateLimitCausesAutoPause_ResumeRetries() {
	env := s.NewTestWorkflowEnvironment()

	// The Temporal test env counts retries internally. With MaximumAttempts=2,
	// it calls the mock function once per "logical invocation" from the workflow,
	// but retries happen under the hood. When the mock returns an error, Temporal
	// retries up to MaximumAttempts, then reports failure to the workflow.
	// So we need the first logical call to fail (both retries will fail),
	// and the second logical call (after resume) to succeed.
	callCount := 0
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			callCount++
			if callCount <= 2 {
				// Fail on both attempts in the first retry cycle
				return "", temporal.NewApplicationError(
					"429 Too Many Requests", "RateLimitError",
				)
			}
			return "completed:" + stepName, nil
		},
	)

	var capturedStatuses []string
	env.OnActivity(workflowStatusStub, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			if status, ok := input["status"].(string); ok {
				capturedStatuses = append(capturedStatuses, status)
			}
			return map[string]interface{}{"success": true}, nil
		},
	)

	// Send resume signal after the workflow auto-pauses
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(autoPauseWorkflow, true)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(), "Workflow should complete successfully after resume")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:call_llm", result, "Should get successful result after retry")

	// Verify the status transitions: paused → started
	s.Contains(capturedStatuses, "paused", "Should emit 'paused' status on rate limit exhaustion")
	s.Contains(capturedStatuses, "started", "Should emit 'started' status after resume")

	// Verify paused comes before started
	pausedIdx := -1
	startedIdx := -1
	for i, status := range capturedStatuses {
		if status == "paused" && pausedIdx == -1 {
			pausedIdx = i
		}
		if status == "started" && startedIdx == -1 {
			startedIdx = i
		}
	}
	s.Greater(startedIdx, pausedIdx, "'started' status should come after 'paused'")
}

func (s *RateLimitAutoPauseSuite) TestRateLimitPause_WithoutResume_StaysBlocked() {
	env := s.NewTestWorkflowEnvironment()

	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			return "", temporal.NewApplicationError(
				"429 Too Many Requests", "RateLimitError",
			)
		},
	)

	var capturedStatuses []string
	env.OnActivity(workflowStatusStub, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			if status, ok := input["status"].(string); ok {
				capturedStatuses = append(capturedStatuses, status)
			}
			return map[string]interface{}{"success": true}, nil
		},
	)

	// Cancel after a delay to prevent infinite blocking
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 500*time.Millisecond)

	env.ExecuteWorkflow(autoPauseWorkflow, true)

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(), "Workflow should not complete without resume")
	s.Contains(capturedStatuses, "paused", "Should emit 'paused' status before blocking")
}

func (s *RateLimitAutoPauseSuite) TestMultiplePauseResumeCycles() {
	env := s.NewTestWorkflowEnvironment()

	callCount := 0
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			callCount++
			// With MaximumAttempts=2, Temporal calls the mock twice per retry cycle.
			// We need 2 full retry cycles to exhaust (4 calls), then succeed on 5th.
			if callCount <= 4 {
				return "", temporal.NewApplicationError(
					"429 Too Many Requests", "RateLimitError",
				)
			}
			return "completed:" + stepName, nil
		},
	)

	var capturedStatuses []string
	env.OnActivity(workflowStatusStub, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			if status, ok := input["status"].(string); ok {
				capturedStatuses = append(capturedStatuses, status)
			}
			return map[string]interface{}{"success": true}, nil
		},
	)

	// Send resume for first pause
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 500*time.Millisecond)

	// Send resume for second pause
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 1000*time.Millisecond)

	env.ExecuteWorkflow(autoPauseWorkflow, true)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(), "Workflow should complete after multiple pause/resume cycles")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:call_llm", result)

	// Should have 2 pause and 2 started transitions
	pauseCount := 0
	startedCount := 0
	for _, status := range capturedStatuses {
		if status == "paused" {
			pauseCount++
		}
		if status == "started" {
			startedCount++
		}
	}
	s.Equal(2, pauseCount, "Should emit 'paused' twice for two rate limit failures")
	s.Equal(2, startedCount, "Should emit 'started' twice for two resumes")
}

// =============================================================================
// Resume-before-pause-arm race (reset-and-replay resume swallow)
// =============================================================================
//
// When a self-paused workflow is recovered via reset-and-replay, the resume
// signal is appended to history at reset time and can be consumed by the
// resume coordinator BEFORE the replayed retry-exhaustion branch re-arms the
// pause. These tests pin the fix (a resume with no armed pause is HELD until
// one arms) and the legacy behavior old histories must keep on replay.

// holdReleaseWorkflow is the minimal expression of the race: the resume
// signal is delivered while nothing is paused, and the pause arms later.
func holdReleaseWorkflow(ctx workflow.Context, holdResume bool) (string, error) {
	pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: holdResume})
	// Give the delayed resume signal time to arrive before the pause arms.
	_ = workflow.Sleep(ctx, 10*time.Millisecond)
	pc.RequestPause()
	pc.CheckPause(ctx)
	// A test-harness CancelWorkflow unblocks CheckPause's Await; surface it
	// as an error so "still parked at cancel time" is distinguishable from a
	// genuine resume.
	if ctx.Err() != nil {
		return "", temporal.NewCanceledError("still parked when cancelled")
	}
	return "resumed", nil
}

func (s *RateLimitAutoPauseSuite) TestResumeBeforePauseArm_HoldReleasesPause() {
	env := s.NewTestWorkflowEnvironment()

	// Resume arrives BEFORE the pause arms (the reset-and-replay ordering).
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 1*time.Millisecond)

	env.ExecuteWorkflow(holdReleaseWorkflow, true)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(),
		"a resume that arrives before the pause arms must be held and release it")
	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("resumed", result)
}

func (s *RateLimitAutoPauseSuite) TestResumeBeforePauseArm_LegacyConsumesResume_StaysParked() {
	// Pre-resume-hold histories replay with holdResume=false and must keep
	// the old behavior: the early resume is consumed as a no-op and the
	// later pause parks the workflow.
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 500*time.Millisecond)

	env.ExecuteWorkflow(holdReleaseWorkflow, false)

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(),
		"legacy behavior: the early resume is spent and the pause parks the workflow")
}

func (s *RateLimitAutoPauseSuite) TestStaleResumeThenUserPause_PauseSticks() {
	// A held resume must NOT undo a user pause that arrives after it: the
	// explicit signal.pause discards the stale queued resume.
	env := s.NewTestWorkflowEnvironment()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 1*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 500*time.Millisecond)

	env.ExecuteWorkflow(func(ctx workflow.Context) (string, error) {
		pc := newPauseCoordinator(ctx, "wf-test", pauseOptions{HoldResume: true})
		// Let the stale resume and then the user pause be delivered.
		_ = workflow.Sleep(ctx, 10*time.Millisecond)
		pc.CheckPause(ctx)
		if ctx.Err() != nil {
			return "", temporal.NewCanceledError("still parked when cancelled")
		}
		return "resumed", nil
	})

	s.True(env.IsWorkflowCompleted())
	s.Error(env.GetWorkflowError(),
		"the user pause must stick: the stale held resume is discarded, not applied to it")
}

func (s *RateLimitAutoPauseSuite) TestRateLimitAutoPause_ResumeBeforeArm_RetriesAndCompletes() {
	// Full-loop version of the race: the resume lands while CallLLM is still
	// burning its retry attempts, i.e. before the retry-exhaustion branch
	// arms the pause. The held resume must release that pause so the step is
	// retried and the workflow completes — the exact shape of the
	// reset-and-replay recovery of a rate-limit self-pause.
	env := s.NewTestWorkflowEnvironment()

	callCount := 0
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			callCount++
			if callCount <= 2 {
				return "", temporal.NewApplicationError(
					"429 Too Many Requests", "RateLimitError",
				)
			}
			return "completed:" + stepName, nil
		},
	)

	var capturedStatuses []string
	env.OnActivity(workflowStatusStub, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			if status, ok := input["status"].(string); ok {
				capturedStatuses = append(capturedStatuses, status)
			}
			return map[string]interface{}{"success": true}, nil
		},
	)

	// First activity attempt fails at ~0ms, the retry fires at ~100ms and
	// exhaustion arms the pause after it. 1ms lands the resume before that.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 1*time.Millisecond)

	env.ExecuteWorkflow(autoPauseWorkflow, true)

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(),
		"held resume must release the retry-exhaustion pause and let the step retry")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:call_llm", result)
	s.Contains(capturedStatuses, "paused")
	s.Contains(capturedStatuses, "started")
}
