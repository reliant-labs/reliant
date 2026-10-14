package scenario

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// A draft is never runnable (specs/workflow-draft-lifecycle.md). The scenario
// runner's workflow loader refuses a draft by slug with an actionable "is a
// draft" message, as StartChat does
// (internal/handlers/chat/workflow_draft_gate_test.go) and preset resolution
// (internal/handlers/preset/preset_draft_gate_test.go).
func TestScenarioWorkflowLoaderRefusesADraft(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
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

	_, err := createScenarioWorkflowLoader(repo, ctx, "test-user", projectID)("gate-draft")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is a draft")
}
