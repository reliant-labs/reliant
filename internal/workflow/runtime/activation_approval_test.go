package runtime

import (
	"context"
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
