//go:build integration

// Copyright (c) 2025 Reliant Labs
// Package privatepipelineharness contains private, opt-in integration fixtures.
// Infrastructure smoke tests are not evidence that the review pipeline works.
package privatepipelineharness

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

type resources struct {
	SQL      *sql.DB
	Temporal client.Client
	Queue    string
}

func requirePrivatePipeline(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("private SQL/Temporal subprocess fixture; run explicitly without -short")
	}
	if os.Getenv("RUN_FUTURECHAT_B") != "1" {
		t.Skip("private integration fixture requires RUN_FUTURECHAT_B=1")
	}
	guardPrivatePostgres(t)
}

func newResources(t *testing.T) *resources {
	t.Helper()
	requirePrivatePipeline(t)
	// Resolve before allocating SQL. An opted-in run must not silently skip or
	// download a CLI over the network when the local prerequisite is missing.
	binary := os.Getenv("TEMPORAL_CLI_PATH")
	if binary == "" {
		binary = "temporal"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		t.Fatalf("local Temporal CLI required (TEMPORAL_CLI_PATH): %v", err)
	}
	// This API creates/migrates a private clone and registers cleanup. It ignores
	// DATABASE_URL, so an inherited production/dev DSN cannot select real data.
	raw, _ := db.NewTestDatabase(t)
	id := uuid.NewString()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		ExistingPath: path,
		LogLevel:     "never",
		ClientOptions: &client.Options{
			Namespace: "futurechat-b-" + id,
		},
	})
	if err != nil {
		t.Fatalf("start private Temporal server: %v", err)
	}
	t.Cleanup(func() {
		if err := srv.Stop(); err != nil {
			t.Errorf("stop owned Temporal server: %v", err)
		}
	})
	return &resources{SQL: raw, Temporal: srv.Client(), Queue: "futurechat-b-" + id}
}

type observedExecution struct {
	WorkflowID string
	RunID      string
	Database   string
}

func infrastructureWorkflow(ctx workflow.Context) (observedExecution, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})
	var out observedExecution
	err := workflow.ExecuteActivity(ctx, "ObservePrivateDatabase").Get(ctx, &out)
	return out, err
}

// TestFutureChatBInProcessSQL executes a concrete activity against real migrated
// SQL in the SDK's in-process workflow environment. It does not prove server
// history, rollover, review authority, publication or author-wait delivery.
func TestFutureChatBInProcessSQL(t *testing.T) {
	requirePrivatePipeline(t)
	raw, _ := db.NewTestDatabase(t)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context) (observedExecution, error) {
		info := activity.GetInfo(ctx)
		out := observedExecution{WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID}
		err := raw.QueryRowContext(ctx, "SELECT current_database()").Scan(&out.Database)
		return out, err
	}, activity.RegisterOptions{Name: "ObservePrivateDatabase"})
	env.ExecuteWorkflow(infrastructureWorkflow)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var got observedExecution
	if err := env.GetWorkflowResult(&got); err != nil {
		t.Fatal(err)
	}
	var database string
	if err := raw.QueryRowContext(t.Context(), "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	if got.Database != database || got.Database == "" || got.WorkflowID == "" || got.RunID == "" {
		t.Fatal("in-process activity did not observe its migrated SQL and execution context")
	}
}

// TestFutureChatBInfrastructure proves only that actual Temporal executes an
// activity against its own migrated SQL database, with an actual exact run ID.
// It deliberately makes no publication, approval, wait or enqueue assertion.
func TestFutureChatBInfrastructure(t *testing.T) {
	f := newResources(t)
	w := worker.New(f.Temporal, f.Queue, worker.Options{})
	w.RegisterWorkflow(infrastructureWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context) (observedExecution, error) {
		info := activity.GetInfo(ctx)
		out := observedExecution{WorkflowID: info.WorkflowExecution.ID, RunID: info.WorkflowExecution.RunID}
		err := f.SQL.QueryRowContext(ctx, "SELECT current_database()").Scan(&out.Database)
		return out, err
	}, activity.RegisterOptions{Name: "ObservePrivateDatabase"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Stop)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	workflowID := "fixture-" + uuid.NewString()
	run, err := f.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: workflowID, TaskQueue: f.Queue,
	}, infrastructureWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var got observedExecution
	if err := run.Get(ctx, &got); err != nil {
		t.Fatal(err)
	}
	var wantDatabase string
	if err := f.SQL.QueryRowContext(ctx, "SELECT current_database()").Scan(&wantDatabase); err != nil {
		t.Fatal(err)
	}
	if got.WorkflowID != workflowID || got.RunID != run.GetRunID() || got.RunID == "" || got.Database != wantDatabase || got.Database == "" {
		t.Fatalf("wrong execution/database: got %+v workflow=%s run=%s database=%s", got, workflowID, run.GetRunID(), wantDatabase)
	}
}
