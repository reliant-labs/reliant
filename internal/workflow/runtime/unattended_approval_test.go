// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

func approvalWorkflowBytes(t *testing.T) []byte {
	t.Helper()
	b, err := protojson.Marshal(&reliantv1.Workflow{
		Name:  "unattended-approval",
		Entry: []string{"confirm"},
		Nodes: []*reliantv1.Node{{
			Id:   "confirm",
			Type: model.NodeTypeApproval,
			Args: &reliantv1.Node_Approval{Approval: &reliantv1.ApprovalArgs{
				Title: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "Deploy?"}},
			}},
		}},
		Outputs: map[string]string{
			"status":        "{{nodes.confirm.status}}",
			"auto_resolved": "{{has(nodes.confirm.auto_resolved) ? nodes.confirm.auto_resolved : false}}",
			"resolved_by":   "{{has(nodes.confirm.resolved_by) ? nodes.confirm.resolved_by : ''}}",
			"record":        "{{has(nodes.confirm.record) ? nodes.confirm.record : ''}}",
		},
	})
	require.NoError(t, err)
	return b
}

func runApprovalWorkflow(t *testing.T, inputs map[string]interface{}) ([]string, WorkflowResult, time.Duration) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	workflowBytes := approvalWorkflowBytes(t)

	var scheduled []string
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		scheduled = append(scheduled, info.ActivityType.Name)
	})
	reg := func(name string, fn interface{}) {
		env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	}
	reg("ApprovalCreate", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"approval_id": "appr-1", "already_resolved": false}, nil
	})
	reg("ApprovalResolve", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	})
	reg("ActivityLoadWorkflow", func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
		return LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
	})
	reg("WorkflowStatus", func(_ context.Context, _ map[string]interface{}) (interface{}, error) { return nil, nil })
	reg("WorkflowCheckpoint", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	})
	reg("Cleanup", func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	})

	start := env.Now()
	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-appr",
		WorkflowName: "unattended-approval",
		Inputs:       inputs,
		ExecContext: &ExecutionContext{
			WorkflowID: "wf-appr", ChatID: "chat-appr", Thread: "thread-appr",
			ThreadMode: model.ThreadModeNew, WorkflowName: "unattended-approval",
		},
	})
	require.NoError(t, env.GetWorkflowError())
	var result WorkflowResult
	require.NoError(t, env.GetWorkflowResult(&result))
	return scheduled, result, env.Now().Sub(start)
}

func TestUnattendedApprovalResolvesImmediatelyWithoutARow(t *testing.T) {
	t.Parallel()
	scheduled, result, elapsed := runApprovalWorkflow(t, map[string]interface{}{"unattended": true})

	require.Less(t, elapsed, time.Minute, "an unattended approval must not wait out defaultApprovalTimeout")
	require.NotContains(t, scheduled, "ApprovalCreate", "no human can act on a pending approval row")
	require.Equal(t, "unattended", result.Outputs["status"])
	require.Equal(t, true, result.Outputs["auto_resolved"])
	require.Equal(t, UnattendedResolver, result.Outputs["resolved_by"])
	require.Contains(t, result.Outputs["record"], UnattendedMarker)
}

func TestAttendedApprovalStillWaitsAndTimesOut(t *testing.T) {
	t.Parallel()
	scheduled, result, elapsed := runApprovalWorkflow(t, map[string]interface{}{})

	require.Contains(t, scheduled, "ApprovalCreate")
	require.GreaterOrEqual(t, elapsed, defaultApprovalTimeout)
	require.Equal(t, "timeout", result.Outputs["status"])
}
