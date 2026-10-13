package preset

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A draft is never runnable (specs/workflow-draft-lifecycle.md), and preset
// resolution is one of the request-side resolvers that refuse a draft by slug
// with an actionable "is a draft" message. StartChat and the scenario
// runner's workflow loader are the others
// (internal/grpc/services/workflow_draft_gate_test.go).
func TestDraftWorkflowIsNeverRunnable(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := AuthedContext(t)
	now := time.Now().UTC()
	projectID := "test-project-draft-gate-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "Draft Gate",
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	// Valid, so the only thing standing between it and running is its status.
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user", Name: "gate-draft", Slug: "gate-draft",
		Definition: `name: gate-draft
entry: [echo]
nodes:
  - id: echo
    type: run
    command: "echo hi"
`,
		Status: db.WorkflowDraftStatusDraft, CreatedAt: now, UpdatedAt: now, Version: 1,
	}))

	svc := NewTestPreset(t, WithPresetDeps(Deps{Repo: repo}))
	_, err := svc.loadWorkflow(ctx, "gate-draft", projectID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a draft")
}
