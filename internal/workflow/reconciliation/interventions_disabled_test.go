// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// The reconciler's three DESTRUCTIVE recovery paths are opt-in and off by
// default: on 2026-09-29 the wedge detector terminated a healthy chat, and its
// six in-flight sub-agents with it, because an overloaded host made the run's
// workflow tasks TIME OUT — the attempt counter it reads cannot tell a timeout
// from a failure. See docs/incidents/2026-09-29-reconciler-false-wedge.md.
//
// These tests drive each detector past its confirmation thresholds with the
// SHIPPED default config and assert that nothing happens: no TerminateWorkflow,
// no ResetWorkflowExecution, no DB status write, no chat message. The
// detection code itself still runs — the point of the switch is that detection
// is free and the response is not.

// disabledWedgeConfig is stuckTestConfig's thresholds with the shipped default
// (interventions off), so the only difference from the terminating tests is
// the switch.
func disabledWedgeConfig(passes int) *ReconcilerConfig {
	cfg := stuckTestConfig(passes)
	cfg.Interventions = false
	return cfg
}

func disabledProgressConfig(detectPasses int) *ReconcilerConfig {
	cfg := progressTestConfig(detectPasses)
	cfg.Interventions = false
	return cfg
}

func TestReconciler_InterventionsDisabled_WedgedWorkflowTask_NoAction(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	terminatedBefore := anomalyCount(anomalyWedgeTerminated)
	reconciler := NewReconciler(repo, tempClient, disabledWedgeConfig(2))
	wf := runningWorkflow()

	// Six passes: the debounce confirms on pass 2 and stays confirmed.
	for i := 1; i <= 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d: interventions disabled, nothing to repair", i)
	}

	assert.Empty(t, tempClient.terminateCalls, "must not terminate a wedged workflow when interventions are off")
	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, repo.updatedStatuses, "DB status must be left alone")
	assert.Empty(t, repo.savedMessages, "no chat message for an action that did not happen")
	assert.Equal(t, terminatedBefore, anomalyCount(anomalyWedgeTerminated),
		"a suppressed action must not be counted as a termination")
}

// A paused wedged run is the same class (a wedged replay can never process its
// resume), so the switch must cover it too.
func TestReconciler_InterventionsDisabled_WedgedPausedWorkflow_NoAction(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeWedgedWorkflowTaskDescribeResp("run-1", 471)},
		},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, disabledWedgeConfig(2))
	wf := runningWorkflow()
	wf.Status = db.Paused()

	for i := 1; i <= 4; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.WasStale, "pass %d", i)
	}

	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
}

func TestReconciler_InterventionsDisabled_StuckActivity_NoResetNoTerminate(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckActivityDescribeResp("run-1", "act-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	resetBefore := anomalyCount(anomalyStuckReset)
	reconciler := NewReconciler(repo, tempClient, disabledWedgeConfig(2))
	wf := runningWorkflow()

	for i := 1; i <= 6; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.RecoveredByReset, "pass %d: no reset when interventions are off", i)
		assert.False(t, result.WasStale, "pass %d", i)
	}

	assert.Empty(t, tempClient.resetCalls, "must not reset a stuck workflow when interventions are off")
	assert.Empty(t, tempClient.terminateCalls, "and must not fall back to terminate either")
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
	assert.Equal(t, resetBefore, anomalyCount(anomalyStuckReset))
}

// The stuck WORKFLOW task class shares the path; cover it so the gate is not
// accidentally scoped to activities.
func TestReconciler_InterventionsDisabled_StuckWorkflowTask_NoResetNoTerminate(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeStuckWorkflowTaskDescribeResp("run-1", 5*time.Minute)},
		},
		historyEvents: makeHistoryWithActivity("act-1"),
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, disabledWedgeConfig(2))
	wf := runningWorkflow()

	for i := 1; i <= 4; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.RecoveredByReset, "pass %d", i)
	}

	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, tempClient.terminateCalls)
	assert.Empty(t, repo.updatedStatuses)
}

func TestReconciler_InterventionsDisabled_ProgressStall_NoTerminate(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	confirmedBefore := anomalyCount(anomalyProgressStallConfirmed)
	// Detect at 2 quiescent passes, confirm at 4.
	reconciler := NewReconciler(repo, tempClient, disabledProgressConfig(2))
	wf := runningWorkflow()

	for i := 1; i <= 8; i++ {
		result := reconciler.ReconcileWorkflow(context.Background(), wf)
		require.NoError(t, result.Error)
		assert.False(t, result.ProgressStalled, "pass %d: interventions disabled", i)
		assert.False(t, result.WasStale, "pass %d", i)
	}

	assert.Empty(t, tempClient.terminateCalls, "must not terminate a stalled workflow when interventions are off")
	assert.Empty(t, tempClient.resetCalls)
	assert.Empty(t, repo.updatedStatuses)
	assert.Empty(t, repo.savedMessages)
	assert.Equal(t, confirmedBefore, anomalyCount(anomalyProgressStallConfirmed),
		"a suppressed action must not be counted as a confirmed-stall termination")
}

// Detection is unaffected by the switch: the DETECTED stage was always
// report-only, so it must keep firing exactly once per streak. This is what
// makes "detect and log, do not act" true rather than "go blind".
func TestReconciler_InterventionsDisabled_ProgressStallStillDetected(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeQuiescentDescribeResp("run-1", 42)},
		},
	}
	tempClient.setPollersActive(true)

	detectedBefore := anomalyCount(anomalyProgressStallDetected)
	reconciler := NewReconciler(repo, tempClient, disabledProgressConfig(2))
	wf := runningWorkflow()

	for i := 1; i <= 6; i++ {
		require.NoError(t, reconciler.ReconcileWorkflow(context.Background(), wf).Error)
	}

	assert.Equal(t, detectedBefore+1, anomalyCount(anomalyProgressStallDetected),
		"the report-only detection stage still fires once per streak")
	assert.Empty(t, tempClient.terminateCalls)
}

// The three gated paths must not swallow the pass: status-drift repair below
// them still has to run. A run that Temporal reports TERMINATED while the DB
// says running is the archetype (a hard terminate produces no completion
// handler, so nothing else will ever tell the user), and it arrives on the
// same pass as a wedged pending task.
func TestReconciler_InterventionsDisabled_StatusDriftStillRepaired(t *testing.T) {
	repo := newMockRepo()
	// Terminated in Temporal, with a pending workflow task at a wedge-level
	// attempt count: the wedge gate must report and fall through, not return.
	desc := makeWedgedWorkflowTaskDescribeResp("run-1", 42)
	desc.WorkflowExecutionInfo.Status = enums.WORKFLOW_EXECUTION_STATUS_TERMINATED
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{"wf-1": {resp: desc}},
	}
	tempClient.setPollersActive(true)

	reconciler := NewReconciler(repo, tempClient, disabledWedgeConfig(2))
	wf := runningWorkflow()

	result := reconciler.ReconcileWorkflow(context.Background(), wf)
	require.NoError(t, result.Error)

	// Terminated is not running, so no wedge/stuck detection even applies —
	// the drift repair is the whole job, and it must have happened.
	assert.True(t, result.WasStale, "terminated-but-running drift must still be repaired")
	assert.Equal(t, db.Failed(), repo.updatedStatuses["wf-1"])
	assert.Empty(t, tempClient.terminateCalls, "the repair must not terminate anything itself")
}

// Lost-workflow repair is one of the unconditional sweeps: a DB row that says
// running with no Temporal record at all must still be closed out.
func TestReconciler_InterventionsDisabled_LostWorkflowStillRepaired(t *testing.T) {
	repo := newMockRepo()
	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{}, // not found
	}

	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	result := reconciler.ReconcileWorkflow(context.Background(), runningWorkflow())
	require.NoError(t, result.Error)
	assert.True(t, result.NeedsRecovery)
	assert.Equal(t, db.Completed(), repo.updatedStatuses["wf-1"])
}

// The shipped default must be off. This is the assertion that fails if someone
// flips it back without reading the incident write-up.
func TestDefaultConfig_InterventionsOff(t *testing.T) {
	assert.False(t, DefaultConfig().Interventions,
		"interventions default off: see docs/incidents/2026-09-29-reconciler-false-wedge.md")
	assert.False(t, NewReconciler(nil, nil, nil).interventions)
	assert.True(t, NewReconciler(nil, nil, &ReconcilerConfig{Interventions: true}).interventions,
		"the switch must be carried from config onto the reconciler")
}
