package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

func TestActivationApprovalFlow(t *testing.T) {
	for _, scenario := range []struct {
		name, status            string
		unattended, interrupted bool
	}{{name: "approved", status: "approved"}, {name: "denied", status: "denied"}, {name: "unattended", unattended: true}, {name: "timeout", status: "timeout"}, {name: "interrupted", interrupted: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			var suite temporaltest.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			creates := 0
			var executed types.RuntimeContext
			env.RegisterActivityWithOptions(func(_ context.Context, in types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
				require.Equal(t, scenario.unattended, in.Unattended)
				if in.Unattended {
					return types.AutomationApprovalPreview{Refusal: "Unattended runs cannot manage activations"}, nil
				}
				return types.AutomationApprovalPreview{Hash: "pinned", Title: "cicd (id machine-1), as owner; workflow privileges"}, nil
			}, activity.RegisterOptions{Name: "AutomationApprovalPreview"})
			env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
				creates++
				require.Contains(t, in["title"], "cicd")
				return map[string]interface{}{"approval_id": "approval"}, nil
			}, activity.RegisterOptions{Name: "ApprovalCreate"})
			env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"success": true}, nil
			}, activity.RegisterOptions{Name: "ApprovalResolve"})
			env.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
				executed = in.Runtime
				return map[string]interface{}{}, nil
			}, activity.RegisterOptions{Name: "ExecuteTools"})
			if !scenario.unattended && scenario.status != "timeout" && !scenario.interrupted {
				env.RegisterDelayedCallback(func() {
					env.SignalWorkflow("signal.approval.approval", map[string]interface{}{"status": scenario.status})
				}, time.Second)
			}
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				child, cancel := workflow.WithCancel(ctx)
				if scenario.interrupted {
					workflow.Go(ctx, func(g workflow.Context) { _ = workflow.Sleep(g, time.Second); cancel() })
				}
				ac := workflow.WithActivityOptions(child, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
				node := &pb.Node{Args: &pb.Node_ExecuteTools{ExecuteTools: &pb.ExecuteToolsArgs{Capabilities: &pb.ToolCapabilities{Offered: []string{names.ToolEnableTrigger}}, ResolvedToolCalls: []*pb.ToolCallMsg{{Id: "call", Name: names.ToolEnableTrigger, Input: `{"trigger_id":"t"}`}}}}}
				return executeWithActivationApprovals(ctx, ac, types.RuntimeContext{ChatID: "chat", Thread: "thread"}, node, scenario.unattended).Get(ctx, nil)
			})
			if scenario.interrupted {
				require.Error(t, env.GetWorkflowError())
				require.Empty(t, executed.ActivationApprovals)
				return
			}
			require.NoError(t, env.GetWorkflowError())
			if scenario.unattended {
				require.Zero(t, creates)
			} else {
				require.Equal(t, 1, creates)
			}
			if scenario.status == "approved" {
				require.Equal(t, "pinned", executed.ActivationApprovals["call"])
				require.Empty(t, executed.ActivationRefusals)
			} else {
				require.Empty(t, executed.ActivationApprovals)
				require.NotEmpty(t, executed.ActivationRefusals["call"])
			}
		})
	}
}

// A turn's calls that each need a card ask for all of them at once, as one
// batch, before waiting on any: the owner sees every card together and can
// decide them together or one by one. Each decision binds only its own call.
func TestActivationApprovalsAskTogether(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var mu sync.Mutex
	var creates []map[string]interface{}
	var executed types.RuntimeContext
	env.RegisterActivityWithOptions(func(_ context.Context, in types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
		if in.ToolCallID == "pause" {
			return types.AutomationApprovalPreview{NoApproval: true}, nil
		}
		return types.AutomationApprovalPreview{Hash: "hash-" + in.ToolCallID, Title: "Approval: " + in.ToolCallID}, nil
	}, activity.RegisterOptions{Name: "AutomationApprovalPreview"})
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		mu.Lock()
		defer mu.Unlock()
		creates = append(creates, in)
		return map[string]interface{}{"approval_id": "approval-" + in["tool_call_id"].(string)}, nil
	}, activity.RegisterOptions{Name: "ApprovalCreate"})
	env.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "ApprovalResolve"})
	env.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
		executed = in.Runtime
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
	// Every card exists before the owner decides any of them.
	env.RegisterDelayedCallback(func() {
		mu.Lock()
		defer mu.Unlock()
		require.Len(t, creates, 3, "every card is raised before the first decision")
		env.SignalWorkflow("signal.approval.approval-a", map[string]interface{}{"status": "approved"})
		env.SignalWorkflow("signal.approval.approval-b", map[string]interface{}{"status": "denied"})
		env.SignalWorkflow("signal.approval.approval-c", map[string]interface{}{"status": "approved"})
	}, time.Second)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ac := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		offered := []string{names.ToolEnableTrigger, names.ToolCreateTriggerActivation, names.ToolPauseTrigger}
		node := &pb.Node{Args: &pb.Node_ExecuteTools{ExecuteTools: &pb.ExecuteToolsArgs{Capabilities: &pb.ToolCapabilities{Offered: offered}, ResolvedToolCalls: []*pb.ToolCallMsg{
			{Id: "a", Name: names.ToolEnableTrigger, Input: `{"trigger_id":"t1"}`},
			{Id: "pause", Name: names.ToolPauseTrigger, Input: `{"trigger_id":"t2"}`},
			{Id: "b", Name: names.ToolCreateTriggerActivation, Input: `{}`},
			{Id: "c", Name: names.ToolEnableTrigger, Input: `{"trigger_id":"t3"}`},
		}}}}
		return executeWithActivationApprovals(ctx, ac, types.RuntimeContext{ChatID: "chat", Thread: "thread"}, node, false).Get(ctx, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	require.Len(t, creates, 3, "the card-free pause raises no card")
	batch, _ := creates[0]["batch_id"].(string)
	require.NotEmpty(t, batch)
	var calls []string
	for _, in := range creates {
		calls = append(calls, in["tool_call_id"].(string))
		require.Equal(t, batch, in["batch_id"], "one batch for the turn")
	}
	require.ElementsMatch(t, []string{"a", "b", "c"}, calls)
	require.Equal(t, map[string]string{"a": "hash-a", "c": "hash-c"}, executed.ActivationApprovals)
	require.Len(t, executed.ActivationRefusals, 1)
	require.Contains(t, executed.ActivationRefusals["b"], "did not approve")
}

// A single card is not a batch.
func TestActivationApprovalSingleCardHasNoBatch(t *testing.T) {
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var created map[string]interface{}
	env.RegisterActivityWithOptions(func(context.Context, types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
		return types.AutomationApprovalPreview{Hash: "h", Title: "Approval: one"}, nil
	}, activity.RegisterOptions{Name: "AutomationApprovalPreview"})
	env.RegisterActivityWithOptions(func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		created = in
		return map[string]interface{}{"approval_id": "approval"}, nil
	}, activity.RegisterOptions{Name: "ApprovalCreate"})
	env.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow("signal.approval.approval", map[string]interface{}{"status": "approved"})
	}, time.Second)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ac := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		node := &pb.Node{Args: &pb.Node_ExecuteTools{ExecuteTools: &pb.ExecuteToolsArgs{Capabilities: &pb.ToolCapabilities{Offered: []string{names.ToolEnableTrigger}}, ResolvedToolCalls: []*pb.ToolCallMsg{{Id: "call", Name: names.ToolEnableTrigger, Input: `{"trigger_id":"t"}`}}}}}
		return executeWithActivationApprovals(ctx, ac, types.RuntimeContext{ChatID: "chat", Thread: "thread"}, node, false).Get(ctx, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "call", created["tool_call_id"])
	require.NotContains(t, created, "batch_id")
}
