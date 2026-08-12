package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
)

func setupTestScenarioService(t *testing.T) (*ScenarioService, *db.Repo, string, string, context.Context) {
	t.Helper()

	repo := db.NewTestRepo(t)
	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now().UTC()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Scenario Test Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	return NewScenarioService(repo, nil), repo, userID, projectID, ctx
}

func TestScenarioService_RunScenario_LoadsReferencedProjectWorkflowInternals(t *testing.T) {
	service, repo, userID, projectID, ctx := setupTestScenarioService(t)
	now := time.Now().UTC()

	parentWorkflow := `name: parent-flow
apiVersion: "1.0"
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: project://child-flow
  - id: done
    type: save_message
    args:
      role: assistant
      content: Done
edges:
  - from: child
    default: done
`
	childWorkflow := `name: child-flow
apiVersion: "1.0"
entry: [draft]
nodes:
  - id: draft
    type: call_llm
    args:
      model: mock
outputs:
  response_text: "{{nodes.draft.response_text}}"
edges: []
`

	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     userID,
		Name:       "Parent Flow",
		Slug:       "parent-flow",
		Definition: parentWorkflow,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}))

	storedWorkflows, err := json.Marshal([]cfg.StoredWorkflow{{
		Slug:        "child-flow",
		Name:        "child-flow",
		YAMLContent: childWorkflow,
	}})
	require.NoError(t, err)
	storedWorkflowsJSON := string(storedWorkflows)
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ID:                   uuid.NewString(),
		ProjectID:            projectID,
		DaemonID:             "test-daemon",
		ProjectWorkflowsJSON: &storedWorkflowsJSON,
		PushedAt:             now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}))

	outputJSON := `{"response_text":"child done"}`
	resp, err := service.RunScenario(ctx, connect.NewRequest(&reliantv1.RunScenarioRequest{
		ProjectId:    projectID,
		WorkflowSlug: "parent-flow",
		Scenario: &reliantv1.ScenarioDefinition{
			Name: "project_ref_internal",
			Events: []*reliantv1.SimulatedEvent{{
				Node:       "child.draft",
				OutputJson: outputJSON,
			}},
			Expect: &reliantv1.ScenarioExpectation{
				Outcome: "completed",
				Reached: []string{"child", "child.draft", "done"},
			},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, "passed", resp.Msg.Result.Status, "mismatches: %v", resp.Msg.Result.Mismatches)
	require.Contains(t, resp.Msg.Result.Execution.NodesReached, "child.draft")
}
