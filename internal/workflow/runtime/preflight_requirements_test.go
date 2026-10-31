// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/requirements"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

var requiresWorkflow = &reliantv1.Workflow{
	Name:     "release",
	Requires: &reliantv1.WorkflowRequirements{Tools: []string{"task"}, Integrations: []string{"github"}, Repos: []string{"reliant"}},
}

// requirementsWorkflow is the slice of DynamicWorkflow under test: the
// requirements check, then (standing in for graph entry) the first node.
func requirementsWorkflow(ctx workflow.Context, wf *reliantv1.Workflow, child bool) error {
	execCtx := &ExecutionContext{}
	if child {
		execCtx.Parent = &ParentContext{}
	}
	input := WorkflowInput{ChatID: "c1", WorkflowName: wf.GetName(), Inputs: map[string]interface{}{InputKeyActingIdentity: "app", InputKeyAutomation: "merged"}}
	if err := checkRequirements(ctx, input, execCtx, wf, "wf1", "wf1"); err != nil {
		return err
	}
	return workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10e9}), "FirstNode").Get(ctx, nil)
}

func newRequirementsEnv() *testsuite.TestWorkflowEnvironment {
	env := (&temporaltest.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	env.RegisterWorkflow(requirementsWorkflow)
	noop := func(map[string]interface{}) (map[string]interface{}, error) { return nil, nil }
	for _, name := range []string{PreflightRequirementsActivityName, "WorkflowError", "FirstNode"} {
		env.RegisterActivityWithOptions(noop, activity.RegisterOptions{Name: name})
	}
	return env
}

func TestRequirementsMetReachTheFirstNode(t *testing.T) {
	env := newRequirementsEnv()
	var got map[string]interface{}
	env.OnActivity(PreflightRequirementsActivityName, mock.Anything).
		Run(func(args mock.Arguments) { got = args.Get(0).(map[string]interface{}) }).
		Return(map[string]interface{}{}, nil).Once()
	env.OnActivity("FirstNode", mock.Anything).Return(map[string]interface{}{}, nil).Once()
	env.ExecuteWorkflow(requirementsWorkflow, requiresWorkflow, false)
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	assert.Equal(t, "app", got["acting_identity"])
	assert.Equal(t, "merged", got["automation"])
	assert.ElementsMatch(t, []interface{}{"task"}, got["tools"])
}

// Unmet requirements fail the run before its first node, with the activity's
// message (every problem) as the run's error and its error card, and are not
// retried.
func TestRequirementsUnmetFailBeforeTheFirstNode(t *testing.T) {
	env := newRequirementsEnv()
	unmet := temporal.NewNonRetryableApplicationError(requirements.Report{Problems: []requirements.Problem{
		{Kind: "tool", Name: "task", Problem: "not found on the machine's PATH", Fix: "install task"},
	}}.Error(), requirements.UnmetErrType, nil)
	env.OnActivity(PreflightRequirementsActivityName, mock.Anything).Return(nil, unmet).Once()
	var card map[string]interface{}
	env.OnActivity("WorkflowError", mock.Anything).
		Run(func(args mock.Arguments) { card = args.Get(0).(map[string]interface{}) }).
		Return(map[string]interface{}{}, nil).Once()
	env.ExecuteWorkflow(requirementsWorkflow, requiresWorkflow, false)
	err := env.GetWorkflowError()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tool task: not found on the machine's PATH. Fix: install task")
	assert.Equal(t, "requirements_unmet", card["error_type"])
	env.AssertNotCalled(t, "FirstNode", mock.Anything)
	env.AssertExpectations(t)
}

// requirementsCompletionWorkflow ends the requirements slice the way
// DynamicWorkflow does, through handleWorkflowCompletion, so the terminal
// status it reports can be read.
func requirementsCompletionWorkflow(ctx workflow.Context, wf *reliantv1.Workflow) (retErr error) {
	defer func() {
		handleWorkflowCompletion(ctx, "wf1", "c1", wf.GetName(), "", "wf1", "", retErr, "", "", false, nil, nil)
	}()
	return requirementsWorkflow(ctx, wf, false)
}

// Unmet requirements are the run's recorded failure: the "failed" status
// carries every problem as its error, which the status activity writes to the
// run's failure_detail (see workflow_status_test), so GetRun and the run page
// say why a run that never reached a node failed.
func TestRequirementsUnmetAreTheFailedStatusError(t *testing.T) {
	env := newRequirementsEnv()
	env.RegisterWorkflow(requirementsCompletionWorkflow)
	env.RegisterActivityWithOptions(workflowStatusStub, activity.RegisterOptions{Name: "WorkflowStatus"})
	env.RegisterActivityWithOptions(cleanupStub, activity.RegisterOptions{Name: "Cleanup"})
	unmet := temporal.NewNonRetryableApplicationError(requirements.Report{Problems: []requirements.Problem{
		{Kind: "tool", Name: "task", Problem: "not found on the machine's PATH", Fix: "install task"},
		{Kind: "integration", Name: "github", Problem: "no connection", Fix: "connect GitHub"},
	}}.Error(), requirements.UnmetErrType, nil)
	env.OnActivity(PreflightRequirementsActivityName, mock.Anything).Return(nil, unmet).Once()
	statuses := map[string]map[string]interface{}{}
	env.OnActivity("WorkflowStatus", mock.Anything, mock.Anything).Return(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			statuses[in["status"].(string)] = in
			return map[string]interface{}{"success": true}, nil
		})

	env.ExecuteWorkflow(requirementsCompletionWorkflow, requiresWorkflow)
	require.True(t, env.IsWorkflowCompleted())

	failed, ok := statuses["failed"]
	require.True(t, ok, "an unmet requirement must end the run failed; got statuses %v", statuses)
	detail, _ := failed["error"].(string)
	assert.Contains(t, detail, "preflight requirements:")
	assert.Contains(t, detail, "tool task: not found on the machine's PATH. Fix: install task")
	assert.Contains(t, detail, "github")
	env.AssertNotCalled(t, "FirstNode", mock.Anything)
}

// A check that could not run is retried, then reported as such, not as unmet.
func TestRequirementsCheckThatCannotRunIsRetriedThenReported(t *testing.T) {
	env := newRequirementsEnv()
	env.OnActivity(PreflightRequirementsActivityName, mock.Anything).
		Return(nil, errors.New("no result came back from the machine")).Times(int(infrastructureActivityMaxAttempts))
	var card map[string]interface{}
	env.OnActivity("WorkflowError", mock.Anything).
		Run(func(args mock.Arguments) { card = args.Get(0).(map[string]interface{}) }).
		Return(map[string]interface{}{}, nil).Once()
	env.ExecuteWorkflow(requirementsWorkflow, requiresWorkflow, false)
	require.Error(t, env.GetWorkflowError())
	assert.Contains(t, card["error_message"], "could not check the run's requirements")
	env.AssertExpectations(t)
}

// No requires:, or a child run (its root checked), schedules nothing.
func TestRequirementsSkippedWithoutDeclarationOrForAChild(t *testing.T) {
	for name, tc := range map[string]struct {
		wf    *reliantv1.Workflow
		child bool
	}{
		"none":  {wf: &reliantv1.Workflow{Name: "plain"}},
		"child": {wf: requiresWorkflow, child: true},
	} {
		t.Run(name, func(t *testing.T) {
			env := newRequirementsEnv()
			env.OnActivity("FirstNode", mock.Anything).Return(map[string]interface{}{}, nil).Once()
			env.ExecuteWorkflow(requirementsWorkflow, tc.wf, tc.child)
			require.NoError(t, env.GetWorkflowError())
			env.AssertNotCalled(t, PreflightRequirementsActivityName, mock.Anything)
		})
	}
}
