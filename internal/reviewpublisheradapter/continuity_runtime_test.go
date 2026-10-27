package reviewpublisheradapter

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/runcontinuity"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func continuationReview(ctx workflow.Context, successor bool) (string, error) {
	if !successor {
		return "", workflow.NewContinueAsNewError(ctx, "PrivateCANAssignment", true)
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	var id string
	if err := workflow.ExecuteActivity(ctx, ResolveAssignmentActivityName).Get(ctx, &id); err != nil {
		return "", err
	}
	// Keep the successor alive while its exact assignment resolution is asserted.
	signal := workflow.GetSignalChannel(ctx, "finish")
	var done bool
	signal.Receive(ctx, &done)
	return id, nil
}

func TestActualTemporalCANResolvesSameSQLAssignment(t *testing.T) {
	if testing.Short() {
		t.Skip("private SQL and pinned Temporal CAN proof; run without -short")
	}
	path := os.Getenv("TEMPORAL_CLI_PATH")
	if path == "" {
		t.Fatal("set verified TEMPORAL_CLI_PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{ExistingPath: path, LogLevel: "never", ClientOptions: &client.Options{Namespace: "default"}})
	require.NoError(t, err)
	defer srv.Stop()
	s, _, _ := publisherFixture(t, "approve")
	queue := "can-" + uuid.NewString()
	execution, err := srv.Client().ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: "review-root", TaskQueue: queue}, "PrivateCANAssignment", false)
	require.NoError(t, err)
	// Fixture enrollment pins actual initial provider execution before polling;
	// no current chat pointer and no replacement assignment are used.
	_, err = s.DB.Bun().NewRaw(`UPDATE review_assignments SET reviewer_run_id=? WHERE id='assignment'`, execution.GetRunID()).Exec(ctx)
	require.NoError(t, err)
	_, err = s.DB.Bun().NewRaw(`UPDATE trigger_events SET admission_run_id=? WHERE id='event'`, execution.GetRunID()).Exec(ctx)
	require.NoError(t, err)
	resolver := ResolveAssignmentActivity{Store: s, History: TemporalEvidence{Service: srv.Client().WorkflowService(), Namespace: "default"}}
	resolved := make(chan string, 1)
	w := worker.New(srv.Client(), queue, worker.Options{})
	w.RegisterWorkflowWithOptions(continuationReview, workflow.RegisterOptions{Name: "PrivateCANAssignment"})
	w.RegisterActivityWithOptions(func(ctx context.Context) (string, error) {
		id, err := resolver.Execute(ctx)
		if err == nil {
			resolved <- id
		}
		return id, err
	}, activity.RegisterOptions{Name: ResolveAssignmentActivityName})
	require.NoError(t, w.Start())
	defer w.Stop()
	select {
	case id := <-resolved:
		require.Equal(t, "assignment", id)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	a, err := s.LoadReviewAuthority(ctx, "assignment")
	require.NoError(t, err)
	require.Equal(t, execution.GetRunID(), a.Binding.Reviewer.RunID, "immutable original remains initial")
	proof, err := runcontinuity.Resolve(ctx, runcontinuity.TemporalReader{Service: srv.Client().WorkflowService(), Namespace: "default"}, runcontinuity.Execution{WorkflowID: "review-root", RunID: execution.GetRunID()})
	require.NoError(t, err)
	require.NotEqual(t, execution.GetRunID(), proof.Current.RunID)
	require.NoError(t, srv.Client().SignalWorkflow(ctx, "review-root", proof.Current.RunID, "finish", true))
	var id string
	require.NoError(t, execution.Get(ctx, &id))
	require.Equal(t, "assignment", id)
}
