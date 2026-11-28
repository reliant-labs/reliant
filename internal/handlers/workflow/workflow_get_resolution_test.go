package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/workflowdraftstest"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// GetWorkflow must show the definition a run of the same name resolves to:
// the caller's own workflow shadows the project's (workflowsource).
func TestGetWorkflow_AgreesWithRunResolution(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	projectID := "test-project-getwf-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: "test-user", Name: "P", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	def := func(desc string) string {
		return "name: shared-flow\ndescription: " + desc + "\nentry: [echo]\nnodes:\n  - id: echo\n    type: run\n    command: \"echo hi\"\n"
	}
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user", Name: "shared-flow", Slug: "shared-flow",
		Definition: def("from-user"), Status: db.WorkflowDraftStatusComplete,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}))
	stored, err := json.Marshal([]cfg.StoredWorkflow{{Slug: "shared-flow", Name: "shared-flow", YAMLContent: def("from-project")}})
	require.NoError(t, err)
	storedJSON := string(stored)
	require.NoError(t, store.UpsertProjectConfigRecord(ctx, repo.ORM(), &db.ProjectConfigRecord{
		ID: uuid.NewString(), ProjectID: projectID, DaemonID: "d",
		ProjectWorkflowsJSON: &storedJSON, PushedAt: now, CreatedAt: now, UpdatedAt: now,
	}))

	run, err := workflowsource.Resolve(ctx, repo, workflowsource.Options{UserID: "test-user", ProjectID: projectID}, "shared-flow")
	require.NoError(t, err)

	svc := NewTestWorkflow(t, WithWorkflowDeps(Deps{Repo: repo, Triggers: &MockTriggers{}, Drafts: workflowdraftstest.New(t, repo), Projects: projectaccesstest.For(repo)}))
	got, err := svc.GetWorkflow(ctx, connect.NewRequest(&reliantv1.GetWorkflowRequest{Name: "shared-flow", ProjectId: projectID}))
	require.NoError(t, err)
	require.Equal(t, string(run.Source), got.Msg.Source)
	require.Equal(t, "user", got.Msg.Source)
	require.Equal(t, string(run.YAML), got.Msg.YamlDefinition)
}
