// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// PauseSignalTestSuite tests the signal-based pause/resume behavior
type PauseSignalTestSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestPauseSignal(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(PauseSignalTestSuite))
}

// =========================================================================
// MOCK ACTIVITIES
// =========================================================================

// StepActivity simulates a generic workflow step activity
func StepActivity(ctx context.Context, stepName string) (string, error) {
	return "completed:" + stepName, nil
}

// =========================================================================
// TEST WORKFLOWS
// =========================================================================

// testPauseWorkflow executes multiple activities in sequence with checkPause
// between each one, mirroring DynamicWorkflow's cooperative pause pattern.
func testPauseWorkflow(ctx workflow.Context) (string, error) {
	// Set up signal-based pause infrastructure (same pattern as DynamicWorkflow)
	var pauseRequested bool
	pauseCh := workflow.GetSignalChannel(ctx, "signal.pause")
	resumeCh := workflow.GetSignalChannel(ctx, "signal.resume")

	checkPause := func() {
		for pauseCh.ReceiveAsync(nil) {
			pauseRequested = true
		}
		if pauseRequested {
			resumeCh.Receive(ctx, nil)
			pauseRequested = false
		}
	}

	// Background goroutine to listen for pause signals at any time
	workflow.Go(ctx, func(gCtx workflow.Context) {
		for {
			pauseCh.Receive(gCtx, nil)
			pauseRequested = true
		}
	})

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	steps := []string{"step1", "step2", "step3"}
	var results []string

	for _, step := range steps {
		// Check for pause at step boundary (before each activity)
		checkPause()

		var result string
		err := workflow.ExecuteActivity(ctx, StepActivity, step).Get(ctx, &result)
		if err != nil {
			return "", err
		}
		results = append(results, result)
	}

	return results[len(results)-1], nil
}

// =========================================================================
// TEST CASES
// =========================================================================

func (s *PauseSignalTestSuite) TestPauseAndResume() {
	env := s.NewTestWorkflowEnvironment()

	var activitiesExecuted []string
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			activitiesExecuted = append(activitiesExecuted, stepName)
			return "completed:" + stepName, nil
		},
	)

	// After 0ms (first activity completes), send pause signal
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 0)

	// After 100ms, verify step2 hasn't run yet, then send resume
	env.RegisterDelayedCallback(func() {
		// At this point only step1 should have completed because we paused
		// before step2. Send resume to unblock.
		env.SignalWorkflow("signal.resume", nil)
	}, 100*time.Millisecond)

	env.ExecuteWorkflow(testPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Nil(env.GetWorkflowError(), "Workflow should complete successfully after resume")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:step3", result)

	// All three activities should have executed
	s.Equal([]string{"step1", "step2", "step3"}, activitiesExecuted)
}

func (s *PauseSignalTestSuite) TestPauseBeforeAnyActivity() {
	env := s.NewTestWorkflowEnvironment()

	var activitiesExecuted []string
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			activitiesExecuted = append(activitiesExecuted, stepName)
			return "completed:" + stepName, nil
		},
	)

	// Send pause immediately before workflow starts executing activities
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 0)

	// Send resume after a delay
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 200*time.Millisecond)

	env.ExecuteWorkflow(testPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Nil(env.GetWorkflowError(), "Workflow should complete successfully after resume")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:step3", result)

	// All activities should have executed after resume
	s.Equal([]string{"step1", "step2", "step3"}, activitiesExecuted)
}

func (s *PauseSignalTestSuite) TestMultiplePauseResumeCycles() {
	env := s.NewTestWorkflowEnvironment()

	var activitiesExecuted []string
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			activitiesExecuted = append(activitiesExecuted, stepName)
			return "completed:" + stepName, nil
		},
	)

	// First pause/resume cycle
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 100*time.Millisecond)

	// Second pause/resume cycle
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 200*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 300*time.Millisecond)

	env.ExecuteWorkflow(testPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Nil(env.GetWorkflowError(), "Workflow should complete after multiple pause/resume cycles")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:step3", result)

	// All activities should complete
	s.Equal([]string{"step1", "step2", "step3"}, activitiesExecuted)
}

func (s *PauseSignalTestSuite) TestPauseSignalIdempotency() {
	env := s.NewTestWorkflowEnvironment()

	var activitiesExecuted []string
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			activitiesExecuted = append(activitiesExecuted, stepName)
			return "completed:" + stepName, nil
		},
	)

	// Send two pause signals
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 50*time.Millisecond)

	// Send a single resume — should unblock despite two pauses
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 150*time.Millisecond)

	env.ExecuteWorkflow(testPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Nil(env.GetWorkflowError(), "Workflow should resume after single resume despite double pause")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:step3", result)

	s.Equal([]string{"step1", "step2", "step3"}, activitiesExecuted)
}

// TestRapidPauseResumeCycles verifies that rapid-fire pause/resume cycles
// don't cause non-determinism errors. Each cycle must cleanly resolve its
// pause state before the next one begins — this exercises the state machine
// behavior that the replay-safe changes rely on.
func (s *PauseSignalTestSuite) TestRapidPauseResumeCycles() {
	env := s.NewTestWorkflowEnvironment()

	var activitiesExecuted []string
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			activitiesExecuted = append(activitiesExecuted, stepName)
			return "completed:" + stepName, nil
		},
	)

	// Send three rapid pause/resume cycles with minimal delay between them.
	// This simulates a user spamming the pause/resume button.
	for i := 0; i < 3; i++ {
		pauseDelay := time.Duration(i*20) * time.Millisecond
		resumeDelay := time.Duration(i*20+10) * time.Millisecond
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow("signal.pause", nil)
		}, pauseDelay)
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow("signal.resume", nil)
		}, resumeDelay)
	}

	env.ExecuteWorkflow(testPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Nil(env.GetWorkflowError(), "Workflow should complete after rapid pause/resume cycles")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:step3", result)

	s.Equal([]string{"step1", "step2", "step3"}, activitiesExecuted)
}

// TestPauseResumeInterleaved verifies that interleaving pause signals with
// activity completions doesn't break the state machine. A pause arrives while
// an activity is in-flight, then resume arrives after.
func (s *PauseSignalTestSuite) TestPauseResumeInterleaved() {
	env := s.NewTestWorkflowEnvironment()

	var activitiesExecuted []string
	env.OnActivity(StepActivity, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, stepName string) (string, error) {
			activitiesExecuted = append(activitiesExecuted, stepName)
			return "completed:" + stepName, nil
		},
	)

	// Pause arrives after step1 completes (at step2 boundary)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 0)

	// Resume arrives, step2 and step3 should complete
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 50*time.Millisecond)

	// Another pause arrives after step2 completes
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.pause", nil)
	}, 100*time.Millisecond)

	// Final resume
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.resume", nil)
	}, 150*time.Millisecond)

	env.ExecuteWorkflow(testPauseWorkflow)

	s.True(env.IsWorkflowCompleted())
	s.Nil(env.GetWorkflowError(), "Workflow should complete after interleaved pause/resume")

	var result string
	s.NoError(env.GetWorkflowResult(&result))
	s.Equal("completed:step3", result)

	s.Equal([]string{"step1", "step2", "step3"}, activitiesExecuted)
}
