//go:build integration

// Copyright (c) 2025 Reliant Labs
package privatepipelineharness

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/encoding/protojson"
)

// Register actual SQL lifecycle handlers, not success-only projections. A fixed
// trusted snapshot loader is the explicit fixture boundary; reviewer completion
// authority must still be derived separately from actual server history.
func registerSQLRuntime(t *testing.T, w worker.Worker, repo *db.Repo, definition string) {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(definition))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := protojson.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	w.RegisterWorkflow(runtime.DynamicWorkflow)
	w.RegisterActivityWithOptions(func(context.Context, handlers.LoadWorkflowInput) (*handlers.LoadWorkflowOutput, error) {
		return &handlers.LoadWorkflowOutput{YAML: []byte(definition), WorkflowJSON: raw, HasTemplates: strings.Contains(definition, "{{")}, nil
	}, activity.RegisterOptions{Name: "ActivityLoadWorkflow"})
	w.RegisterActivityWithOptions(handlers.NewWorkflowStatusActivity(repo).Execute, activity.RegisterOptions{Name: "WorkflowStatus"})
	w.RegisterActivityWithOptions(handlers.NewWorkflowCheckpointActivity(repo).Execute, activity.RegisterOptions{Name: "WorkflowCheckpoint"})
	w.RegisterActivityWithOptions(handlers.NewCleanupActivity(repo).Execute, activity.RegisterOptions{Name: "Cleanup"})
}

func TestFutureChatBActualDynamicRuntimeSQL(t *testing.T) {
	f := newResources(t)
	repo, err := db.NewRepoWithDriver(f.SQL, db.DriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	root := uuid.NewString()
	if err := repo.CreateChat(t.Context(), &db.Chat{ID: root, Title: "private runtime", ProjectID: "test-project", UserID: "test-user", WorkflowID: &root}); err != nil {
		t.Fatal(err)
	}
	definition := `name: private-runtime
entry: [finish]
nodes:
  - id: finish
    type: compute
    outcome: success
    outputs:
      proof: "{{ 'concrete-runtime' }}"
outputs:
  proof: "{{ nodes.finish.proof }}"
`
	w := worker.New(f.Temporal, f.Queue, worker.Options{})
	registerSQLRuntime(t, w, repo, definition)
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	run, err := f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: root, TaskQueue: f.Queue}, runtime.DynamicWorkflow, runtime.WorkflowInput{
		ChatID: root, WorkflowName: "private-runtime", Inputs: map[string]any{},
		ExecContext: &runtime.ExecutionContext{ChatID: root, Thread: root, ThreadMode: "new", WorkflowName: "private-runtime"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result runtime.WorkflowResult
	if err := run.Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outputs["proof"] != "concrete-runtime" {
		t.Fatalf("actual runtime did not execute compute node: %#v", result.Outputs)
	}
	persisted, err := repo.GetWorkflow(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != db.Completed() || persisted.Outcome == nil || *persisted.Outcome != "success" {
		t.Fatalf("actual lifecycle handlers did not persist completed graph outcome: %+v", persisted)
	}
}
