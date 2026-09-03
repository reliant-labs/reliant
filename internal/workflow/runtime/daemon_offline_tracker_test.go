// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// ============================================================================
// Test fixtures
// ============================================================================

const offlineToolResultContent = "Failed to execute tool on daemon: unavailable: no daemon connected for user"

func offlineToolResultsEvent() *StepEvent {
	return &StepEvent{
		StepID: "execute_tools",
		Data: map[string]interface{}{
			"tool_results": []interface{}{
				map[string]interface{}{
					"is_error": true,
					"content":  offlineToolResultContent,
				},
			},
		},
	}
}

func successToolResultsEvent() *StepEvent {
	return &StepEvent{
		StepID: "execute_tools",
		Data: map[string]interface{}{
			"tool_results": []interface{}{
				map[string]interface{}{
					"is_error": false,
					"content":  `{"stdout":"ok","stderr":"","exit_code":0}`,
				},
			},
		},
	}
}

func mixedToolResultsEvent() *StepEvent {
	return &StepEvent{
		StepID: "execute_tools",
		Data: map[string]interface{}{
			"tool_results": []interface{}{
				map[string]interface{}{
					"is_error": true,
					"content":  offlineToolResultContent,
				},
				map[string]interface{}{
					"is_error": false,
					"content":  `{"stdout":"partial","stderr":"","exit_code":0}`,
				},
			},
		},
	}
}

func nonDaemonErrorResultsEvent() *StepEvent {
	return &StepEvent{
		StepID: "execute_tools",
		Data: map[string]interface{}{
			"tool_results": []interface{}{
				map[string]interface{}{
					"is_error": true,
					"content":  `Response tool schema validation failed: property "slides" has type "string", want "array"`,
				},
			},
		},
	}
}

func callLLMEvent() *StepEvent {
	return &StepEvent{
		StepID: "call_llm",
		Data: map[string]interface{}{
			"message": map[string]interface{}{"role": "assistant", "text": "thinking..."},
		},
	}
}

// ============================================================================
// Circuit breaker unit tests
// ============================================================================

func TestDaemonOfflineCircuitBreaker_NilSafe(t *testing.T) {
	t.Parallel()
	var b *DaemonOfflineCircuitBreaker
	// Must be no-ops on nil receiver, never panic.
	b.ObserveStep(nil, "ExecuteTools", offlineToolResultsEvent())
	if got := b.ConsecutiveOffline(); got != 0 {
		t.Errorf("nil breaker ConsecutiveOffline = %d, want 0", got)
	}

	// A nil PauseController (simulator/tests) must also be a safe no-op.
	var pc *PauseController
	pc.ObserveDaemonOfflineStep(nil, "ExecuteTools", offlineToolResultsEvent())
	(&PauseController{}).ObserveDaemonOfflineStep(nil, "ExecuteTools", offlineToolResultsEvent())
}

func TestDaemonOfflineCircuitBreaker_CounterAndPauseSemantics(t *testing.T) {
	t.Parallel()
	type step struct {
		activityName string
		event        *StepEvent
	}

	offline := func() step { return step{"ExecuteTools", offlineToolResultsEvent()} }

	tests := []struct {
		name            string
		steps           []step
		wantPauseCount  int
		wantPauseStreak []int // streak values passed to the pause callback, in order
		wantFinalStreak int
	}{
		{
			name:            "three consecutive offline steps trigger pause exactly at threshold",
			steps:           []step{offline(), offline(), offline()},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 3,
		},
		{
			name:            "below threshold never pauses",
			steps:           []step{offline(), offline()},
			wantPauseCount:  0,
			wantFinalStreak: 2,
		},
		{
			name: "a successful tool call resets the streak",
			steps: []step{
				offline(), offline(),
				{"ExecuteTools", successToolResultsEvent()},
				offline(), offline(), offline(),
			},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 3,
		},
		{
			name: "mixed offline+success step resets the streak (daemon partially back)",
			steps: []step{
				offline(), offline(),
				{"ExecuteTools", mixedToolResultsEvent()},
				offline(),
			},
			wantPauseCount:  0,
			wantFinalStreak: 1,
		},
		{
			name: "call_llm steps between failures do not reset the streak",
			steps: []step{
				offline(),
				{"CallLLM", callLLMEvent()},
				offline(),
				{"CallLLM", callLLMEvent()},
				offline(),
			},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 3,
		},
		{
			name: "non-daemon tool errors are neutral (no bump, no reset)",
			steps: []step{
				offline(),
				{"ExecuteTools", nonDaemonErrorResultsEvent()},
				offline(),
				{"ExecuteTools", nonDaemonErrorResultsEvent()},
				offline(),
			},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 3,
		},
		{
			name: "successful run step resets the streak",
			steps: []step{
				offline(), offline(),
				{"ExecuteRunStep", &StepEvent{StepID: "run", Data: map[string]interface{}{"exit_code": float64(0)}}},
				offline(),
			},
			wantPauseCount:  0,
			wantFinalStreak: 1,
		},
		{
			name: "go-level step errors are neutral (retry-exhaustion machinery owns them)",
			steps: []step{
				offline(), offline(),
				{"ExecuteRunStep", &StepEvent{StepID: "run", Error: errors.New("checking daemon status: unavailable: no daemon connected for user")}},
				offline(),
			},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 3,
		},
		{
			name: "streak not reset by pausing: next offline step re-pauses after resume",
			steps: []step{
				offline(), offline(), offline(), // pause at 3
				offline(), // resumed, still offline → immediate re-pause at 4
			},
			wantPauseCount:  2,
			wantPauseStreak: []int{3, 4},
			wantFinalStreak: 4,
		},
		{
			name: "after pause a daemon success resets the streak",
			steps: []step{
				offline(), offline(), offline(), // pause at 3
				{"ExecuteTools", successToolResultsEvent()}, // daemon back
				offline(),
			},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 1,
		},
		{
			name: "nil event and empty outputs are neutral",
			steps: []step{
				offline(),
				{"ExecuteTools", nil},
				{"SaveMessage", &StepEvent{StepID: "save", Data: map[string]interface{}{}}},
				offline(), offline(),
			},
			wantPauseCount:  1,
			wantPauseStreak: []int{3},
			wantFinalStreak: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pauseStreaks []int
			b := NewDaemonOfflineCircuitBreaker(DaemonOfflinePauseThreshold, func(_ workflow.Context, streak int) {
				pauseStreaks = append(pauseStreaks, streak)
			})

			for _, s := range tt.steps {
				b.ObserveStep(nil, s.activityName, s.event)
			}

			if len(pauseStreaks) != tt.wantPauseCount {
				t.Errorf("pause fired %d times (streaks %v), want %d", len(pauseStreaks), pauseStreaks, tt.wantPauseCount)
			}
			if tt.wantPauseStreak != nil {
				for i, want := range tt.wantPauseStreak {
					if i >= len(pauseStreaks) {
						break
					}
					if pauseStreaks[i] != want {
						t.Errorf("pause call %d fired at streak %d, want %d", i, pauseStreaks[i], want)
					}
				}
			}
			if got := b.ConsecutiveOffline(); got != tt.wantFinalStreak {
				t.Errorf("final streak = %d, want %d", got, tt.wantFinalStreak)
			}
		})
	}
}

func TestDaemonOfflineCircuitBreaker_NilPauseCallbackOnlyCounts(t *testing.T) {
	t.Parallel()
	b := NewDaemonOfflineCircuitBreaker(DaemonOfflinePauseThreshold, nil)
	for i := 0; i < 5; i++ {
		b.ObserveStep(nil, "ExecuteTools", offlineToolResultsEvent())
	}
	if got := b.ConsecutiveOffline(); got != 5 {
		t.Errorf("streak = %d, want 5", got)
	}
}

// ============================================================================
// E2E: breaker pauses (blocking) and resumes inside a Temporal workflow
// ============================================================================

type DaemonOfflinePauseSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestDaemonOfflinePause(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(DaemonOfflinePauseSuite))
}

// stepActivityForPause is a fake "step" activity used by the pause test
// workflow. It is always mocked per-test.
func stepActivityForPause(ctx context.Context, mode string) (map[string]interface{}, error) {
	return nil, fmt.Errorf("stub: should be mocked")
}

type daemonPauseTestResult struct {
	PauseCount  int `json:"pause_count"`
	FinalStreak int `json:"final_streak"`
}

// daemonPauseTestWorkflow mirrors the StepExecutor.HandleCompletion seam:
// each "step" runs one activity and feeds its outcome to the breaker. The
// pause callback blocks on a "test.resume" signal — the same shape as the
// real callback, which blocks on the pause epoch until signal.resume.
func daemonPauseTestWorkflow(ctx workflow.Context, modes []string) (daemonPauseTestResult, error) {
	pauseCount := 0
	resumeCh := workflow.GetSignalChannel(ctx, "test.resume")

	breaker := NewDaemonOfflineCircuitBreaker(DaemonOfflinePauseThreshold, func(callerCtx workflow.Context, streak int) {
		pauseCount++
		// Block until "resumed" — proves the workflow re-enters cleanly and
		// keeps processing subsequent steps after a breaker-initiated pause.
		resumeCh.Receive(callerCtx, nil)
	})

	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	})

	for _, mode := range modes {
		var out map[string]interface{}
		err := workflow.ExecuteActivity(actCtx, stepActivityForPause, mode).Get(ctx, &out)
		breaker.ObserveStep(ctx, "ExecuteTools", &StepEvent{
			StepID: "execute_tools",
			Data:   out,
			Error:  err,
		})
	}

	return daemonPauseTestResult{
		PauseCount:  pauseCount,
		FinalStreak: breaker.ConsecutiveOffline(),
	}, nil
}

func (s *DaemonOfflinePauseSuite) TestPausesAtThresholdAndResumesCleanly() {
	env := s.NewTestWorkflowEnvironment()

	env.OnActivity(stepActivityForPause, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, mode string) (map[string]interface{}, error) {
			switch mode {
			case "offline":
				return map[string]interface{}{
					"tool_results": []interface{}{
						map[string]interface{}{
							"is_error": true,
							"content":  offlineToolResultContent,
						},
					},
				}, nil
			case "ok":
				return map[string]interface{}{
					"tool_results": []interface{}{
						map[string]interface{}{
							"is_error": false,
							"content":  `{"stdout":"ok","stderr":"","exit_code":0}`,
						},
					},
				}, nil
			}
			return nil, fmt.Errorf("unexpected mode %s", mode)
		},
	)

	// Steps: 3 offline (→ pause at streak 3), then another offline (→ streak
	// is NOT reset by pausing, so it re-pauses immediately at 4), then a
	// success (→ streak resets to 0).
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("test.resume", nil) }, time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("test.resume", nil) }, 2*time.Second)

	env.ExecuteWorkflow(daemonPauseTestWorkflow, []string{"offline", "offline", "offline", "offline", "ok"})

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(), "breaker pauses must be resumable, not terminal")

	var result daemonPauseTestResult
	require.NoError(s.T(), env.GetWorkflowResult(&result))
	s.Equal(2, result.PauseCount, "pause at streak 3, re-pause at streak 4 after resume")
	s.Equal(0, result.FinalStreak, "final success resets the streak")
}

func (s *DaemonOfflinePauseSuite) TestDoesNotPauseBelowThreshold() {
	env := s.NewTestWorkflowEnvironment()

	callCount := 0
	env.OnActivity(stepActivityForPause, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, mode string) (map[string]interface{}, error) {
			callCount++
			if callCount <= 2 {
				return map[string]interface{}{
					"tool_results": []interface{}{
						map[string]interface{}{
							"is_error": true,
							"content":  offlineToolResultContent,
						},
					},
				}, nil
			}
			// Third step succeeds → reset, never reaches the threshold.
			return map[string]interface{}{
				"tool_results": []interface{}{
					map[string]interface{}{
						"is_error": false,
						"content":  `{"stdout":"ok","stderr":"","exit_code":0}`,
					},
				},
			}, nil
		},
	)

	env.ExecuteWorkflow(daemonPauseTestWorkflow, []string{"a", "b", "c"})

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError())

	var result daemonPauseTestResult
	require.NoError(s.T(), env.GetWorkflowResult(&result))
	s.Equal(0, result.PauseCount, "two offline steps then success must not pause")
	s.Equal(0, result.FinalStreak)
}
