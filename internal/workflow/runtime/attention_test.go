package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func TestDynamicWorkflowAttentionStartAndTerminalReplayGate(t *testing.T) {
	definition := strings.Replace(noOutcomeYAML, "entry: [work]", "attention:\n  key: incident\n  on_start: recovering\n  recovery_timeout: 30m\n  on_success: resolved\nentry: [work]", 1)
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "old-history"}[legacy], func(t *testing.T) {
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			rec := setupResumeEnv(t, env, definition)
			if legacy {
				env.OnGetVersion(attentionChangeID, workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
			}
			input := resumeWorkflowInput("attention-chat", nil)
			input.Inputs[InputKeyLaunchRun], input.Inputs[InputKeyUnattended] = true, true
			env.ExecuteWorkflow(DynamicWorkflow, input)
			require.NoError(t, env.GetWorkflowError())
			var updates []core.AttentionUpdate
			for _, status := range rec.statuses {
				if raw, ok := status["attention"]; ok {
					encoded, err := json.Marshal(raw)
					require.NoError(t, err)
					var update core.AttentionUpdate
					require.NoError(t, json.Unmarshal(encoded, &update))
					updates = append(updates, update)
				}
			}
			if legacy {
				require.Empty(t, updates)
				return
			}
			require.Len(t, updates, 2)
			require.Equal(t, "start", updates[0].Phase)
			require.Equal(t, "recovering", updates[0].State)
			require.Equal(t, "finish", updates[1].Phase)
			require.Equal(t, "resolved", updates[1].State)
			require.NotEmpty(t, updates[0].RunID)
			require.False(t, updates[0].StartedAt.IsZero())
			require.Equal(t, updates[0].RunID, updates[1].RunID)
			require.Equal(t, updates[0].StartedAt, updates[1].StartedAt)
		})
	}
}

func TestDynamicWorkflowAttentionTerminalPersistenceExhaustionIsExecutionFailure(t *testing.T) {
	definition := strings.Replace(terminalOutcomeYAML, "entry: [work]", "attention:\n  key: incident\nentry: [work]", 1)
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, definition)
	env.OnActivity("WorkflowStatus", mock.Anything, mock.Anything).Return(func(_ context.Context, status map[string]interface{}) (interface{}, error) {
		rec.statuses = append(rec.statuses, status)
		if status["status"] == "completed" {
			return nil, temporal.NewNonRetryableApplicationError("receipt unavailable", "PersistenceExhausted", nil)
		}
		return nil, nil
	})
	input := resumeWorkflowInput("attention-terminal-error", nil)
	input.Inputs[InputKeyLaunchRun], input.Inputs[InputKeyUnattended] = true, true
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.Error(t, env.GetWorkflowError())
	require.Contains(t, env.GetWorkflowError().Error(), "persist workflow attention terminal status")
	final := terminalStatus(t, rec)
	require.Equal(t, "completed", final["status"], "attempted lifecycle does not erase declared job verdict")
	require.Equal(t, "failure", final["outcome"])
}

func TestDynamicWorkflowAttentionInvalidKeyFailsDurablyWithoutSuppression(t *testing.T) {
	definition := strings.Replace(noOutcomeYAML, "entry: [work]", "attention:\n  key: '{{ inputs.missing }}'\nentry: [work]", 1)
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rec := setupResumeEnv(t, env, definition)
	env.RegisterActivityWithOptions(func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) { return nil, nil }, activity.RegisterOptions{Name: "WorkflowError"})
	input := resumeWorkflowInput("attention-invalid", nil)
	input.Inputs[InputKeyLaunchRun], input.Inputs[InputKeyUnattended] = true, true
	env.ExecuteWorkflow(DynamicWorkflow, input)
	require.Error(t, env.GetWorkflowError())
	final := terminalStatus(t, rec)
	require.Equal(t, "failed", final["status"])
	require.Contains(t, final["error"], "attention.key")
	require.NotContains(t, final, "attention", "invalid policy input must not register legacy suppression")
	require.Empty(t, rec.executedNodes)
}

func TestResolveAttentionKey(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		inputs    map[string]interface{}
		want      string
		fails     bool
	}{
		{name: "correlated", key: "repo:{{ inputs.repo }}:pr:{{ inputs.pr }}", inputs: map[string]interface{}{"repo": "org/repo", "pr": 12}, want: "repo:org/repo:pr:12"},
		{name: "literal", key: "incident", want: "incident"},
		{name: "empty", key: "{{ inputs.key }}", inputs: map[string]interface{}{"key": ""}, fails: true},
		{name: "whitespace", key: "  ", fails: true},
		{name: "missing", key: "{{ inputs.missing }}", fails: true},
		{name: "non-string", key: "{{ inputs.key }}", inputs: map[string]interface{}{"key": 12}, fails: true},
		{name: "bounded", key: strings.Repeat("x", 513), fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveAttention(&reliantv1.WorkflowAttention{Key: tc.key}, tc.inputs)
			if tc.fails {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAttentionTransitionsDoNotRewriteOutcome(t *testing.T) {
	at := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	a := &runAttention{policy: &reliantv1.WorkflowAttention{Key: "incident", OnStart: "recovering", RecoveryTimeout: "30m", OnSuccess: "resolved"}, key: "incident", runID: "immutable-run", startedAt: at}
	for _, tc := range []struct{ status, outcome, state, class string }{
		{"started", "", "recovering", ""},
		{"completed", model.OutcomeSuccess, "resolved", ""},
		{"completed", model.OutcomeFailure, "action_required", "unsuccessful"},
		{"failed", "", "action_required", "execution_error"},
		{"cancelled", "", "action_required", "execution_error"},
	} {
		t.Run(tc.status+tc.outcome, func(t *testing.T) {
			u := a.update("chat", tc.status, tc.outcome, "detail")
			require.NotNil(t, u)
			require.Equal(t, tc.state, u.State)
			require.Equal(t, tc.class, u.FailureClass)
			require.Equal(t, at, u.StartedAt)
			require.Equal(t, "immutable-run", u.RunID)
			if tc.status == "started" {
				require.Equal(t, 30*time.Minute, u.RecoveryTimeout)
				require.Equal(t, "start", u.Phase)
			} else {
				require.Equal(t, "finish", u.Phase)
			}
		})
	}
	require.Nil(t, a.update("chat", "paused", "", ""))
}

func TestLoadTimeResolutionPreservesAttentionKey(t *testing.T) {
	raw := map[string]interface{}{"name": "example", "attention": map[string]interface{}{"key": "{{ inputs.missing }}"}}
	resolved, err := ResolveWorkflowTemplates(raw, map[string]interface{}{}, nil)
	require.NoError(t, err, "only the dedicated policy path evaluates correlation")
	require.Equal(t, raw["attention"], resolved["attention"])
}

func attentionContinuationProbe(ctx workflow.Context, handoff AttentionHandoff) error {
	a := &runAttention{policy: handoff.Policy, key: handoff.Key, runID: handoff.RunID, startedAt: handoff.StartedAt}
	ctx = workflow.WithValue(ctx, attentionContextKey{}, a)
	return newContinueAsNewError(ctx, WorkflowInput{ChatID: "chat", Inputs: map[string]interface{}{InputKeyLaunchRun: true}}, "loop", 7, nil, false)
}

func TestAttentionContinueAsNewSerializesOriginalOwner(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	handoff := AttentionHandoff{Policy: &reliantv1.WorkflowAttention{Key: "frozen", OnStart: "recovering", RecoveryTimeout: "30m"}, Key: "frozen", RunID: "original-run", StartedAt: time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)}
	env.ExecuteWorkflow(attentionContinuationProbe, handoff)
	var contErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &contErr))
	var carried WorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &carried))
	require.NotNil(t, carried.AttentionHandoff)
	require.Equal(t, handoff.RunID, carried.AttentionHandoff.RunID)
	require.Equal(t, handoff.StartedAt, carried.AttentionHandoff.StartedAt)
	require.Equal(t, handoff.Key, carried.AttentionHandoff.Key)
	require.Equal(t, "30m", carried.AttentionHandoff.Policy.GetRecoveryTimeout())
}

func TestAttentionContinuationRetainsFrozenKeyAndLease(t *testing.T) {
	at := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	policy := &reliantv1.WorkflowAttention{Key: "{{ inputs.key }}", OnStart: "recovering", RecoveryTimeout: "30m", OnSuccess: "resolved"}
	handoff := &AttentionHandoff{Policy: policy, Key: "original", RunID: "original-run", StartedAt: at}
	key, err := attentionKeyForRun(policy, map[string]interface{}{"key": "new-input"}, handoff)
	require.NoError(t, err)
	require.Equal(t, "original", key)
	// Even a changed/missing input cannot move correlation on history compaction.
	key, err = attentionKeyForRun(policy, nil, handoff)
	require.NoError(t, err)
	require.Equal(t, "original", key)
	a := &runAttention{policy: handoff.Policy, key: key, runID: handoff.RunID, startedAt: handoff.StartedAt}
	u := a.update("chat", "started", "", "")
	require.Equal(t, at.Add(30*time.Minute), u.StartedAt.Add(u.RecoveryTimeout))
	require.Equal(t, "original-run", u.RunID, "duplicate start cannot become a new recovery owner")
	late := lateWakeSuccessorInput(WorkflowInput{Inputs: map[string]interface{}{InputKeyLaunchRun: true}, AttentionHandoff: handoff})
	require.Nil(t, late.AttentionHandoff)
	require.False(t, IsLaunchRun(late.Inputs), "a person's late reply is not a repair continuation")
}

func TestAttentionNoImplicitResolution(t *testing.T) {
	a := &runAttention{policy: &reliantv1.WorkflowAttention{Key: "incident"}, key: "incident"}
	require.Empty(t, a.update("chat", "completed", "", "").State, "observer success never resolves another failure")
	a.policy.OnStart = "recovering"
	require.Equal(t, "action_required", a.update("chat", "completed", "", "").State, "owner cannot exit leaving suppression behind")
	var absent *runAttention
	require.Nil(t, absent.update("chat", "failed", "", ""))
}
