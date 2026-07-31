package db

import (
	"context"
	"testing"
	"time"
)

// TestSetWorkflowOutcomeRoundTrips proves the verdict survives the schema: the
// migration added the column, the writer sets it, and every SELECT * read path
// returns it. Without persistence, status/analyze would have to re-derive the
// verdict from a mutable workflow definition.
func TestSetWorkflowOutcomeRoundTrips(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-outcome"
	if err := repo.CreateChat(ctx, &Chat{ID: chatID, ProjectID: "test-project", Title: "t", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	wf := &Workflow{ID: "wf-outcome", ChatID: chatID, WorkflowName: "builtin://forge-one-shot", Thread: "t-1", Status: WorkflowStatusRunning, CreatedAt: time.Now().UTC()}
	if err := repo.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	got, err := repo.GetWorkflow(ctx, "wf-outcome")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome != nil {
		t.Fatalf("a fresh workflow must declare no outcome, got %q", *got.Outcome)
	}

	if err := repo.SetWorkflowOutcome(ctx, "wf-outcome", "failure"); err != nil {
		t.Fatalf("set outcome: %v", err)
	}
	if err := repo.UpdateWorkflowStatus(ctx, "wf-outcome", WorkflowStatusCompleted); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, err = repo.GetWorkflow(ctx, "wf-outcome")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Outcome == nil || *got.Outcome != "failure" {
		t.Fatalf("outcome = %v, want failure", got.Outcome)
	}
	if got.Status != WorkflowStatusCompleted {
		t.Fatalf("status = %v, want completed — the two facts must coexist", got.Status)
	}
}
