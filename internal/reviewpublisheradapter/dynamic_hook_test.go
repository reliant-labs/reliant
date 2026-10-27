package reviewpublisheradapter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
)

// This proof uses the ACTUAL DynamicWorkflow engine and private hook. The graph
// loader/ordinary action and assignment lookup are controlled fixtures, not SQL
// or LLM integration claims. CompletionActivity and exact Temporal histories are real.
func TestActualDynamicWorkflowPrivateHook(t *testing.T) {
	if testing.Short() {
		t.Skip("boots pinned private Temporal server; run without -short")
	}
	path := os.Getenv("TEMPORAL_CLI_PATH")
	if path == "" {
		t.Fatal("TEMPORAL_CLI_PATH must name verified CLI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{ExistingPath: path, LogLevel: "never", ClientOptions: &client.Options{Namespace: "default"}})
	require.NoError(t, err)
	defer srv.Stop()
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			wf := &reliantv1.Workflow{Name: "trusted", Entry: []string{"review"}, Nodes: []*reliantv1.Node{{Id: "review", Type: model.NodeTypeAction, Outcome: outcome}}, Outputs: map[string]string{"review_verdict": "approve"}}
			graph, err := protojson.Marshal(wf)
			require.NoError(t, err)
			yaml := []byte("name: trusted\nentry: [review]\nnodes:\n  - id: review\n    type: action\n    outcome: " + outcome + "\noutputs:\n  review_verdict: approve\n")
			snapshot, err := json.Marshal(GraphSnapshot{WorkflowType: "PrivateTrustedReviewer", RootRef: "trusted", Graphs: map[string]json.RawMessage{"trusted": graph}, YAML: map[string][]byte{"trusted": yaml}, CompletionNode: ReviewCompletionPath, CompletionActivity: CompletionActivityName, OutputSchema: CompletionSchema})
			require.NoError(t, err)
			digest, err := store.DigestReviewSnapshot(snapshot)
			require.NoError(t, err)
			root := "review-" + uuid.NewString()
			queue := "review-" + uuid.NewString()
			execution, err := srv.Client().ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: queue}, "PrivateTrustedReviewer", runtime.WorkflowInput{ChatID: "chat", WorkflowName: "trusted", Inputs: map[string]interface{}{}, ExecContext: &runtime.ExecutionContext{ChatID: "chat", Thread: "thread", ThreadMode: model.ThreadModeNew, WorkflowName: "trusted"}})
			require.NoError(t, err)
			fixture := &proofStore{assignment: store.ReviewAuthority{ID: "assignment", Binding: reviewpublisher.Binding{Reviewer: reviewpublisher.Execution{RootID: root, RunID: execution.GetRunID()}, WorkflowDigest: digest}, GraphSnapshot: snapshot, CompletionNode: ReviewCompletionPath}, pending: make(chan store.ReviewEvidence, 4)}
			reader := TemporalEvidence{Service: srv.Client().WorkflowService(), Namespace: "default"}
			completion := CompletionActivity{Store: fixture, History: reader}
			w := worker.New(srv.Client(), queue, worker.Options{})
			w.RegisterWorkflowWithOptions(PrivateReviewWorkflow, workflow.RegisterOptions{Name: "PrivateTrustedReviewer"})
			w.RegisterActivityWithOptions(func(context.Context, map[string]string) (runtime.LoadedWorkflow, error) {
				return runtime.LoadedWorkflow{YAML: yaml, WorkflowJSON: graph}, nil
			}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
			w.RegisterActivityWithOptions(func(context.Context, types.ActivityInput) (map[string]any, error) {
				return map[string]any{"ok": true}, nil
			}, activity.RegisterOptions{Name: "Action"})
			w.RegisterActivityWithOptions(func(context.Context) (string, error) { return "assignment", nil }, activity.RegisterOptions{Name: ResolveAssignmentActivityName})
			w.RegisterActivityWithOptions(completion.Execute, activity.RegisterOptions{Name: CompletionActivityName})
			w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) {
				return map[string]any{"daemon_available": true, "daemon_id": "fixture"}, nil
			}, activity.RegisterOptions{Name: "PreflightDaemonCheck"})
			for _, name := range []string{"WorkflowStatus", "WorkflowCheckpoint", "Cleanup"} {
				w.RegisterActivityWithOptions(func(context.Context, map[string]any) (map[string]any, error) { return map[string]any{}, nil }, activity.RegisterOptions{Name: name})
			}
			require.NoError(t, w.Start())
			defer w.Stop()
			var output runtime.WorkflowResult
			require.NoError(t, execution.Get(ctx, &output))
			require.Equal(t, "approve", output.Outputs["review_verdict"])
			var pending store.ReviewEvidence
			select {
			case pending = <-fixture.pending:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			require.False(t, pending.GraphSucceeded)
			require.False(t, pending.Terminal)
			final, err := reader.Verify(ctx, fixture.assignment, pending.RunID, pending.ActivityID, 0, true)
			require.NoError(t, err)
			require.True(t, final.Terminal)
			require.Equal(t, outcome == "success", final.GraphSucceeded)
		})
	}
}
