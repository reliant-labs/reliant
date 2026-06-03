// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// ============================================================================
// Tracker unit tests
// ============================================================================

func TestDaemonOfflineTracker_NilSafe(t *testing.T) {
	var tr *DaemonOfflineTracker

	// All methods must be no-ops on nil receiver, never panic.
	tr.Reset()
	tr.ObserveStepError(errors.New("anything"))
	tr.ObserveStepOutput("step", map[string]interface{}{})
	tr.ObserveDaemonSuccess("step")
	if got := tr.ObserveTurnBoundary(); got != 0 {
		t.Errorf("nil tracker ObserveTurnBoundary = %d, want 0", got)
	}
	if got := tr.ConsecutiveOfflineTurns(); got != 0 {
		t.Errorf("nil tracker ConsecutiveOfflineTurns = %d, want 0", got)
	}
}

func TestDaemonOfflineTracker_CounterSemantics(t *testing.T) {
	// Each "turn" is a Reset+observations+ObserveTurnBoundary cycle.
	type observation struct {
		// stepErr is observed via ObserveStepError. Empty = no error.
		stepErr string
		// stepOutput is observed via ObserveStepOutput. nil = none.
		stepOutput map[string]interface{}
		// daemonSuccess records ObserveDaemonSuccess (e.g. ExecuteRunStep ok).
		daemonSuccess bool
	}

	// One "turn" = a list of observations recorded between Reset and
	// ObserveTurnBoundary.
	type turn struct {
		obs           []observation
		wantStreakEnd int
	}

	offlineErr := errors.New("checking daemon status: unavailable: no daemon connected for user")
	offlineToolResult := map[string]interface{}{
		"tool_results": []interface{}{
			map[string]interface{}{
				"is_error": true,
				"content":  "Failed to execute tool on daemon: unavailable: no daemon connected for user",
			},
		},
	}
	successToolResult := map[string]interface{}{
		"tool_results": []interface{}{
			map[string]interface{}{
				"is_error": false,
				"content":  `{"stdout":"hi","stderr":"","exit_code":0}`,
			},
		},
	}
	mixedToolResult := map[string]interface{}{
		"tool_results": []interface{}{
			map[string]interface{}{
				"is_error": true,
				"content":  "Failed to execute tool on daemon: unavailable: no daemon connected for user",
			},
			map[string]interface{}{
				"is_error": false,
				"content":  `{"stdout":"partial","stderr":"","exit_code":0}`,
			},
		},
	}
	callLLMOutput := map[string]interface{}{
		"message": map[string]interface{}{"role": "assistant", "text": "thinking..."},
	}

	tests := []struct {
		name  string
		turns []turn
	}{
		{
			name: "three consecutive daemon-offline turns increments to 3",
			turns: []turn{
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 1},
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 2},
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 3},
			},
		},
		{
			name: "daemon success resets streak mid-flight",
			turns: []turn{
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 1},
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 2},
				{obs: []observation{{daemonSuccess: true}}, wantStreakEnd: 0},
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 1},
			},
		},
		{
			name: "call_llm-only turn doesn't reset streak (no daemon evidence either way)",
			turns: []turn{
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 1},
				{obs: []observation{{stepOutput: callLLMOutput}}, wantStreakEnd: 1},
				{obs: []observation{{stepErr: offlineErr.Error()}}, wantStreakEnd: 2},
			},
		},
		{
			name: "daemon-offline tool_result in execute_tools output increments streak",
			turns: []turn{
				{obs: []observation{{stepOutput: offlineToolResult}}, wantStreakEnd: 1},
				{obs: []observation{{stepOutput: offlineToolResult}}, wantStreakEnd: 2},
			},
		},
		{
			name: "mixed tool_result (one fail + one succeed) resets streak",
			turns: []turn{
				{obs: []observation{{stepOutput: offlineToolResult}}, wantStreakEnd: 1},
				{obs: []observation{{stepOutput: offlineToolResult}}, wantStreakEnd: 2},
				{obs: []observation{{stepOutput: mixedToolResult}}, wantStreakEnd: 0},
			},
		},
		{
			name: "successful execute_tools alone doesn't reset (no proof daemon was involved)",
			turns: []turn{
				{obs: []observation{{stepOutput: offlineToolResult}}, wantStreakEnd: 1},
				{obs: []observation{{stepOutput: successToolResult}}, wantStreakEnd: 1},
				{obs: []observation{{stepOutput: offlineToolResult}}, wantStreakEnd: 2},
			},
		},
		{
			name: "non-daemon error doesn't increment streak",
			turns: []turn{
				{obs: []observation{{stepErr: "rate limit 429"}}, wantStreakEnd: 0},
				{obs: []observation{{stepErr: "rate limit 429"}}, wantStreakEnd: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewDaemonOfflineTracker()
			for i, turn := range tt.turns {
				tr.Reset()
				for _, ob := range turn.obs {
					if ob.stepErr != "" {
						tr.ObserveStepError(errors.New(ob.stepErr))
					}
					if ob.stepOutput != nil {
						tr.ObserveStepOutput(fmt.Sprintf("step-%d", i), ob.stepOutput)
					}
					if ob.daemonSuccess {
						tr.ObserveDaemonSuccess(fmt.Sprintf("step-%d", i))
					}
				}
				got := tr.ObserveTurnBoundary()
				if got != turn.wantStreakEnd {
					t.Errorf("turn %d: streak = %d, want %d", i, got, turn.wantStreakEnd)
				}
			}
		})
	}
}

// ============================================================================
// HaltError marker check
// ============================================================================

func TestHaltError_CarriesMarker(t *testing.T) {
	err := HaltError(3)
	if err == nil {
		t.Fatal("HaltError(3) returned nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, DaemonOfflineHaltMarker) {
		t.Errorf("HaltError message missing marker %q: %s", DaemonOfflineHaltMarker, msg)
	}
	if !strings.Contains(msg, "3 consecutive turns") {
		t.Errorf("HaltError message missing turn count: %s", msg)
	}
}

// ============================================================================
// E2E: workflow halts after threshold via Temporal test env
// ============================================================================

type DaemonOfflineHaltSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
}

func TestDaemonOfflineHalt(t *testing.T) {
	suite.Run(t, new(DaemonOfflineHaltSuite))
}

// stepActivityForHalt is a fake "step" activity used by the halt test workflow.
// It returns whatever the test mocks return.
func stepActivityForHalt(ctx context.Context, mode string) (map[string]interface{}, error) {
	return nil, fmt.Errorf("stub: should be mocked")
}

// daemonHaltTestWorkflow mirrors the per-turn loop seam in DynamicWorkflow but
// strips out everything except the daemon-offline tracker and the
// step-completion observation. Each "turn" runs a single activity whose
// outcome is determined by the test mock. The workflow halts itself when the
// tracker's streak meets DaemonOfflineHaltThreshold, returning the same
// terminal error that the real workflow uses.
//
// Returns the streak count at termination so tests can assert the boundary.
func daemonHaltTestWorkflow(ctx workflow.Context, modes []string) (int, error) {
	logger := workflow.GetLogger(ctx)
	tracker := NewDaemonOfflineTracker()

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	actCtx := workflow.WithActivityOptions(ctx, ao)

	for turn := 0; turn < len(modes); turn++ {
		tracker.Reset()

		var result map[string]interface{}
		err := workflow.ExecuteActivity(actCtx, stepActivityForHalt, modes[turn]).Get(ctx, &result)
		if err != nil {
			tracker.ObserveStepError(err)
		} else {
			tracker.ObserveStepOutput(modes[turn], result)
		}

		streak := tracker.ObserveTurnBoundary()
		logger.Info("turn complete", "turn", turn, "streak", streak)
		if streak >= DaemonOfflineHaltThreshold {
			return streak, HaltError(streak)
		}
	}

	return tracker.ConsecutiveOfflineTurns(), nil
}

func (s *DaemonOfflineHaltSuite) TestHaltsAfterThresholdConsecutiveOfflineTurns() {
	env := s.NewTestWorkflowEnvironment()

	offlineErr := temporal.NewNonRetryableApplicationError(
		"checking daemon status: unavailable: no daemon connected for user",
		"DaemonOfflineError",
		nil,
	)

	env.OnActivity(stepActivityForHalt, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, mode string) (map[string]interface{}, error) {
			return nil, offlineErr
		},
	)

	// Run 5 turns — workflow should halt at turn 3.
	env.ExecuteWorkflow(daemonHaltTestWorkflow, []string{"offline", "offline", "offline", "offline", "offline"})

	s.True(env.IsWorkflowCompleted())
	wfErr := env.GetWorkflowError()
	s.Require().Error(wfErr)
	s.Contains(wfErr.Error(), DaemonOfflineHaltMarker, "halt error should carry marker for frontend")
	s.Contains(wfErr.Error(), "3 consecutive turns")
}

func (s *DaemonOfflineHaltSuite) TestDoesNotHaltBelowThreshold() {
	env := s.NewTestWorkflowEnvironment()

	callCount := 0
	env.OnActivity(stepActivityForHalt, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, mode string) (map[string]interface{}, error) {
			callCount++
			// Two offline turns, then success — must NOT halt (threshold is 3).
			if callCount <= 2 {
				return nil, fmt.Errorf("checking daemon status: unavailable: no daemon connected for user")
			}
			return map[string]interface{}{
				"tool_results": []interface{}{
					map[string]interface{}{
						"is_error": false,
						"content":  `{"stdout":"ok","stderr":"","exit_code":0}`,
					},
					map[string]interface{}{
						"is_error": true,
						"content":  "Failed to execute tool on daemon: unavailable: no daemon connected for user",
					},
				},
			}, nil
		},
	)

	env.ExecuteWorkflow(daemonHaltTestWorkflow, []string{"offline", "offline", "mixed"})

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(), "workflow should complete normally without halting")

	var finalStreak int
	require.NoError(s.T(), env.GetWorkflowResult(&finalStreak))
	s.Equal(0, finalStreak, "mixed success+offline turn resets streak to 0")
}

func (s *DaemonOfflineHaltSuite) TestSuccessResetsStreak() {
	env := s.NewTestWorkflowEnvironment()

	// Sequence: offline, offline, daemon-success, offline, offline → finalStreak 2, no halt.
	callCount := 0
	env.OnActivity(stepActivityForHalt, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, mode string) (map[string]interface{}, error) {
			callCount++
			switch callCount {
			case 1, 2, 4, 5:
				return nil, fmt.Errorf("checking daemon status: unavailable: no daemon connected for user")
			case 3:
				// A clean execute_tools success that also includes one
				// daemon-offline result inside — the mixed-result case that
				// proves the daemon is reachable for at least one tool.
				return map[string]interface{}{
					"tool_results": []interface{}{
						map[string]interface{}{
							"is_error": true,
							"content":  "Failed to execute tool on daemon: unavailable: no daemon connected for user",
						},
						map[string]interface{}{
							"is_error": false,
							"content":  `{"stdout":"ok","stderr":"","exit_code":0}`,
						},
					},
				}, nil
			}
			return nil, fmt.Errorf("unexpected call %d", callCount)
		},
	)

	env.ExecuteWorkflow(daemonHaltTestWorkflow, []string{"a", "b", "c", "d", "e"})

	s.True(env.IsWorkflowCompleted())
	s.NoError(env.GetWorkflowError(), "streak reset to 0 in middle turn → never reaches threshold")

	var finalStreak int
	require.NoError(s.T(), env.GetWorkflowResult(&finalStreak))
	s.Equal(2, finalStreak, "two offline turns after reset")
}

// Keep activity registered so test env doesn't complain about unregistered
// activity in suites that share the workflow function.
func init() {
	// Activities are registered per-env in each suite via env.OnActivity, so
	// this init is intentionally a no-op. Left as a marker that the
	// stepActivityForHalt function is the activity-shaped target the
	// daemonHaltTestWorkflow dispatches through.
	_ = activity.GetInfo
}
