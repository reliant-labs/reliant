package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

// runProposals runs one turn of propose_connection calls through the card
// flow, answering each raised card with answer[tool call id] (nil: never).
func runProposals(t *testing.T, calls []*pb.ToolCallMsg, answer map[string]map[string]interface{}) ([]map[string]interface{}, types.RuntimeContext) {
	t.Helper()
	return runProposalsWith(t, calls, answer, func(in types.AutomationApprovalInput) types.AutomationApprovalPreview {
		p := &core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack", Reason: in.ToolInput}
		return types.AutomationApprovalPreview{Hash: "hash-" + in.ToolCallID, Title: "The agent asks you to connect Slack.", Kind: types.PreviewKindConnectionProposal, Connection: p}
	})
}

func runProposalsWith(t *testing.T, calls []*pb.ToolCallMsg, answer map[string]map[string]interface{}, preview func(types.AutomationApprovalInput) types.AutomationApprovalPreview) ([]map[string]interface{}, types.RuntimeContext) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var mu sync.Mutex
	var creates []map[string]interface{}
	var executed types.RuntimeContext
	env.RegisterActivityWithOptions(func(_ context.Context, in types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
		return preview(in), nil
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
	env.RegisterDelayedCallback(func() {
		for id, signal := range answer {
			env.SignalWorkflow("signal.approval.approval-"+id, signal)
		}
	}, time.Second)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ac := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		node := &pb.Node{Args: &pb.Node_ExecuteTools{ExecuteTools: &pb.ExecuteToolsArgs{Capabilities: &pb.ToolCapabilities{Offered: []string{names.ToolProposeConnection}}, ResolvedToolCalls: calls}}}
		return executeWithActivationApprovals(ctx, ac, types.RuntimeContext{ChatID: "chat", Thread: "thread"}, node, false).Get(ctx, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	return creates, executed
}

func proposeCall(id string) *pb.ToolCallMsg {
	return &pb.ToolCallMsg{Id: id, Name: names.ToolProposeConnection, Input: `{"provider":"slack","reason":"x"}`}
}

// propose_connection raises a card carrying its proposal; approving it with
// the connection the person made hands the call that connection's id, and
// nothing else.
func TestConnectionProposalCardConnected(t *testing.T) {
	creates, executed := runProposals(t, []*pb.ToolCallMsg{proposeCall("call")}, map[string]map[string]interface{}{
		"call": {"status": "approved", "connection_id": "conn-1"},
	})
	require.Len(t, creates, 1, "the call raises its card")
	require.Equal(t, "call", creates[0]["tool_call_id"])
	require.NotNil(t, creates[0]["connection_proposal"], "the card records what it asks for")
	require.Equal(t, map[string]string{"call": "hash-call"}, executed.ActivationApprovals)
	require.Equal(t, map[string]string{"call": "conn-1"}, executed.ApprovedConnections)
	require.Empty(t, executed.ActivationRefusals)
}

// Declining, closing the dialog, or nobody answering each answer the call
// with a status, and record no connection.
func TestConnectionProposalCardNotConnected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal map[string]interface{}
		want   string
	}{
		{"declined", map[string]interface{}{"status": "denied"}, "status: declined"},
		{"dialog closed", map[string]interface{}{"status": "denied", "action_taken": "not_completed"}, "status: not completed. The person closed the connection dialog"},
		{"approved without a connection", map[string]interface{}{"status": "approved"}, "status: not completed"},
		{"expired", nil, "status: not completed. Nobody connected Slack"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := map[string]map[string]interface{}{}
			if tc.signal != nil {
				answer["call"] = tc.signal
			}
			_, executed := runProposals(t, []*pb.ToolCallMsg{proposeCall("call")}, answer)
			require.Empty(t, executed.ActivationApprovals)
			require.Empty(t, executed.ApprovedConnections)
			require.Contains(t, executed.ActivationRefusals["call"], tc.want)
		})
	}
}

// Identical proposals in one turn raise one card; the rest are answered as
// already pending.
func TestConnectionProposalDuplicateInATurnIsDeduped(t *testing.T) {
	creates, executed := runProposals(t, []*pb.ToolCallMsg{proposeCall("a"), proposeCall("b")}, map[string]map[string]interface{}{
		"a": {"status": "approved", "connection_id": "conn-1"},
	})
	require.Len(t, creates, 1)
	require.Equal(t, "a", creates[0]["tool_call_id"])
	require.NotContains(t, creates[0], "batch_id", "a lone card is not a batch")
	require.Contains(t, executed.ActivationRefusals["b"], "status: already pending")
	require.Equal(t, map[string]string{"a": "conn-1"}, executed.ApprovedConnections)
}

// A preview that says it is a connection proposal but carries no proposal is
// a server fault: the call is refused with that said, no card is raised, and
// it is never settled as a generic approval (which would drop the connection).
func TestConnectionProposalPreviewWithoutItsProposalFailsLoudly(t *testing.T) {
	creates, executed := runProposalsWith(t, []*pb.ToolCallMsg{proposeCall("call")}, nil, func(in types.AutomationApprovalInput) types.AutomationApprovalPreview {
		return types.AutomationApprovalPreview{Hash: "hash-" + in.ToolCallID, Title: "The agent asks you to connect Slack.", Kind: types.PreviewKindConnectionProposal}
	})
	require.Empty(t, creates, "no card is raised")
	require.Empty(t, executed.ActivationApprovals)
	require.Equal(t, missingProposalRefusal, executed.ActivationRefusals["call"])
}

// The kind, not the payload, decides: a proposal-kind card missing its
// payload at settlement is refused, never approved generically.
func TestConnectionProposalSettledByKind(t *testing.T) {
	refusals := map[string]string{}
	approvals := map[string]string{}
	ask := activationApprovalAsk{rtx: &types.RuntimeContext{ActivationApprovals: approvals, ActivationRefusals: refusals}, proposals: true}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		card := activationCard{call: proposeCall("call"), preview: types.AutomationApprovalPreview{Hash: "h", Kind: types.PreviewKindConnectionProposal}}
		ask.settle(ctx, card, map[string]interface{}{"status": "approved", "connection_id": "conn-1"}, nil)
		return nil
	})
	require.NoError(t, env.GetWorkflowError())
	require.Empty(t, approvals)
	require.Equal(t, missingProposalRefusal, refusals["call"])
}
