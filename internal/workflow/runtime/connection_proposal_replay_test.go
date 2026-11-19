// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// Settling propose_connection cards as connection proposals is gated by
// connectionProposalChangeID. These tests record real histories on a Temporal
// dev server and replay them with the SDK's replayer:
//
//   - a history recorded by the code before the change (the gate off, and no
//     GetVersion call) that resolves a generic activation approval replays
//     through the production gate with no non-determinism;
//   - a history recorded by the current code that settles a proposal carries
//     the approving connection's id to ExecuteTools, and replays.

const approvalProbeWorkflowName = "activation-approval-probe"

// legacyProposalGate is the code before connectionProposalChangeID: no
// GetVersion call, and every card settled the generic way.
func legacyProposalGate(workflow.Context) bool { return false }

func approvalProbe(gate func(workflow.Context) bool) func(workflow.Context, string) (map[string]string, error) {
	return func(ctx workflow.Context, tool string) (map[string]string, error) {
		ac := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		node := &pb.Node{Args: &pb.Node_ExecuteTools{ExecuteTools: &pb.ExecuteToolsArgs{
			Capabilities:      &pb.ToolCapabilities{Offered: []string{tool}},
			ResolvedToolCalls: []*pb.ToolCallMsg{{Id: "call", Name: tool, Input: `{}`}},
		}}}
		// The future is set with the ExecuteTools result as a generic map.
		var result map[string]interface{}
		err := executeWithActivationApprovalsGated(ctx, ac, types.RuntimeContext{ChatID: "chat", Thread: "thread"}, node, false, gate).Get(ctx, &result)
		out := map[string]string{}
		for k, v := range result {
			out[k], _ = v.(string)
		}
		return out, err
	}
}

// approvalProbeActivities are the activities the probe calls: a preview that
// is a connection proposal for propose_connection and a generic card for
// anything else, a card store, and an ExecuteTools that reports what the run
// recorded for the call.
func approvalProbeActivities(w worker.Worker) {
	w.RegisterActivityWithOptions(func(_ context.Context, in types.AutomationApprovalInput) (types.AutomationApprovalPreview, error) {
		if in.ToolName == names.ToolProposeConnection {
			return types.AutomationApprovalPreview{Hash: "hash", Title: "Connect Slack", Kind: types.PreviewKindConnectionProposal,
				Connection: &core.ConnectionProposal{IntegrationID: "slack", IntegrationName: "Slack"}}, nil
		}
		return types.AutomationApprovalPreview{Hash: "hash", Title: "Turn on PR review"}, nil
	}, activity.RegisterOptions{Name: "AutomationApprovalPreview"})
	w.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"approval_id": "approval"}, nil
	}, activity.RegisterOptions{Name: "ApprovalCreate"})
	w.RegisterActivityWithOptions(func(context.Context, map[string]interface{}) (map[string]interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}, activity.RegisterOptions{Name: "ApprovalResolve"})
	w.RegisterActivityWithOptions(func(_ context.Context, in types.ActivityInput) (map[string]string, error) {
		return map[string]string{
			"approval":   in.Runtime.ActivationApprovals["call"],
			"connection": in.Runtime.ApprovedConnections["call"],
			"refusal":    in.Runtime.ActivationRefusals["call"],
		}, nil
	}, activity.RegisterOptions{Name: "ExecuteTools"})
}

// recordApprovalProbe runs the probe registered with gate for tool, answers
// its card with signal, and returns the result and the recorded history.
func recordApprovalProbe(t *testing.T, s *canProbeServer, gate func(workflow.Context) bool, queue, tool string, signal map[string]interface{}) (map[string]string, *historypb.History) {
	t.Helper()
	w := worker.New(s.client, queue, temporaltest.WorkerOptions(worker.Options{}))
	w.RegisterWorkflowWithOptions(approvalProbe(gate), workflow.RegisterOptions{Name: approvalProbeWorkflowName})
	approvalProbeActivities(w)
	require.NoError(t, w.Start())
	defer w.Stop()
	run, err := s.client.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: queue, TaskQueue: queue, WorkflowExecutionTimeout: 2 * time.Minute,
	}, approvalProbeWorkflowName, tool)
	require.NoError(t, err)
	require.NoError(t, s.client.SignalWorkflow(context.Background(), run.GetID(), run.GetRunID(), "signal.approval.approval", signal))
	var out map[string]string
	require.NoError(t, run.Get(context.Background(), &out))
	return out, s.history(run.GetID(), run.GetRunID())
}

func replayApprovalProbe(h *historypb.History, gate func(workflow.Context) bool) error {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflowWithOptions(approvalProbe(gate), workflow.RegisterOptions{Name: approvalProbeWorkflowName})
	return r.ReplayWorkflowHistory(tlog.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), h)
}

// A run recorded before the change, resolving a generic approval, replays
// through the gated code unchanged.
func TestConnectionProposalGateReplaysAnOldGenericApproval(t *testing.T) {
	s := startCANProbeServer(t)
	out, h := recordApprovalProbe(t, s, legacyProposalGate, "approval-legacy", names.ToolEnableTrigger, map[string]interface{}{"status": "approved"})
	require.Equal(t, "hash", out["approval"])
	require.NoError(t, replayApprovalProbe(h, connectionProposalsEnabled), "an old history replays through the production gate")
}

// A proposal settled by the current code hands the approving connection's id
// to the call, and its history replays.
func TestConnectionProposalGateCarriesTheConnection(t *testing.T) {
	s := startCANProbeServer(t)
	out, h := recordApprovalProbe(t, s, connectionProposalsEnabled, "approval-current", names.ToolProposeConnection, map[string]interface{}{"status": "approved", "connection_id": "conn-1"})
	require.Equal(t, map[string]string{"approval": "hash", "connection": "conn-1", "refusal": ""}, out)
	require.NoError(t, replayApprovalProbe(h, connectionProposalsEnabled))
}
