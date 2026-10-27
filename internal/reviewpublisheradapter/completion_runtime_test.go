package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// proofStore captures the production CompletionActivity pending boundary only.
// It is not a fake returning trusted Completion: the real activity verifies
// exact history and loaded graph first, and the real logical workflow closes.
type proofStore struct {
	assignment store.ReviewAuthority
	pending    chan store.ReviewEvidence
}

func (s *proofStore) LoadReviewAuthority(context.Context, string) (store.ReviewAuthority, error) {
	return s.assignment, nil
}
func (s *proofStore) RecordReviewEvidence(_ context.Context, e store.ReviewEvidence) error {
	s.pending <- e
	return nil
}
func (s *proofStore) PrepareIntent(context.Context, reviewpublisher.Intent) (reviewpublisher.Receipt, error) {
	return reviewpublisher.Receipt{}, errors.New("not part of history proof")
}

// deterministicReviewGraph is a real Temporal graph with fixed load, reserved
// completion and logical terminal bookkeeping. A controlled other-branch failure
// after the reserved activity must not turn its earlier approve into success.
func deterministicReviewGraph(ctx workflow.Context, otherBranchFails bool) (map[string]any, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 20 * time.Second})
	var loaded struct {
		WorkflowJSON []byte `json:"workflow_json"`
	}
	if err := workflow.ExecuteActivity(ctx, "ActivityLoadWorkflow", map[string]string{"workflow_name": "trusted"}).Get(ctx, &loaded); err != nil {
		return nil, err
	}
	in := CompletionInput{AssignmentID: "assignment", Runtime: types.RuntimeContext{WorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID, StepID: ReviewCompletionPath, NodePath: ReviewCompletionPath}, Result: GraphResult{Verdict: reviewpublisher.Approve}}
	completionCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{ActivityID: "private-review-completion", StartToCloseTimeout: 20 * time.Second})
	if err := workflow.ExecuteActivity(completionCtx, CompletionActivityName, in).Get(ctx, nil); err != nil {
		return nil, err
	}
	outcome := "success"
	if otherBranchFails {
		outcome = "failure"
	}
	if err := workflow.ExecuteActivity(ctx, "WorkflowStatus", map[string]string{"workflow_id": workflow.GetInfo(ctx).WorkflowExecution.ID, "status": "completed", "outcome": outcome}).Get(ctx, nil); err != nil {
		return nil, err
	}
	return map[string]any{"outputs": map[string]any{"review_verdict": "approve"}}, nil
}

func TestActualTemporalCompletionActivityAndFailedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("boots private Temporal dev server; run without -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	options := testsuite.DevServerOptions{LogLevel: "never", ClientOptions: &client.Options{Namespace: "default"}}
	if path := os.Getenv("TEMPORAL_CLI_PATH"); path != "" {
		options.ExistingPath = path
	} else if path, err := exec.LookPath("temporal"); err == nil {
		options.ExistingPath = path
	} else {
		t.Fatal("set TEMPORAL_CLI_PATH to the verified pinned CLI; no dynamic SDK download")
	}
	srv, err := testsuite.StartDevServer(ctx, options)
	require.NoError(t, err)
	defer srv.Stop()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "approve", true: "failed_other_branch"}[failed], func(t *testing.T) {
			taskQueue := "review-proof-" + uuid.NewString()
			workflowID := "review-root-" + uuid.NewString()
			graph := json.RawMessage(`{"name":"trusted"}`)
			snapshot, err := json.Marshal(GraphSnapshot{WorkflowType: "PrivateReviewProof", RootRef: "trusted", Graphs: map[string]json.RawMessage{"trusted": graph}, YAML: map[string][]byte{"trusted": []byte("name: trusted\nnodes: []\n")}, CompletionNode: ReviewCompletionPath, CompletionActivity: CompletionActivityName, OutputSchema: CompletionSchema})
			require.NoError(t, err)
			digest, err := store.DigestReviewSnapshot(snapshot)
			require.NoError(t, err)
			// Start before polling: exact initial execution ID is authoritative enrollment,
			// not inferred from a current chat pointer or activity supplied args.
			execution, err := srv.Client().ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflowID, TaskQueue: taskQueue}, "PrivateReviewProof", failed)
			require.NoError(t, err)
			fixture := &proofStore{assignment: store.ReviewAuthority{ID: "assignment", Binding: reviewpublisher.Binding{Reviewer: reviewpublisher.Execution{RootID: workflowID, RunID: execution.GetRunID()}, WorkflowDigest: digest}, GraphSnapshot: snapshot, CompletionNode: ReviewCompletionPath}, pending: make(chan store.ReviewEvidence, 4)}
			reader := TemporalEvidence{Service: srv.Client().WorkflowService(), Namespace: "default"}
			completion := CompletionActivity{Store: fixture, History: reader}
			w := worker.New(srv.Client(), taskQueue, worker.Options{})
			w.RegisterWorkflowWithOptions(deterministicReviewGraph, workflow.RegisterOptions{Name: "PrivateReviewProof"})
			w.RegisterActivityWithOptions(func(context.Context, map[string]string) (map[string]any, error) {
				return map[string]any{"workflow_json": []byte(graph), "yaml": []byte("name: trusted\nnodes: []\n"), "has_templates": false}, nil
			}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
			w.RegisterActivityWithOptions(completion.Execute, activity.RegisterOptions{Name: CompletionActivityName})
			w.RegisterActivityWithOptions(func(context.Context, map[string]string) error { return nil }, activity.RegisterOptions{Name: "WorkflowStatus"})
			require.NoError(t, w.Start())
			defer w.Stop()
			var output map[string]any
			require.NoError(t, execution.Get(ctx, &output))
			var pending store.ReviewEvidence
			select {
			case pending = <-fixture.pending:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.False(t, pending.Terminal)
			require.False(t, pending.GraphSucceeded)
			final, err := reader.Verify(ctx, fixture.assignment, pending.RunID, pending.ActivityID, 0, true)
			require.NoError(t, err)
			require.True(t, final.Terminal)
			require.Equal(t, !failed, final.GraphSucceeded)
		})
	}
}
