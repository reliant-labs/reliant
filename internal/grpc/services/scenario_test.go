package services

import (
	"context"
	"encoding/json"
	"strings"
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
		Status:     db.WorkflowDraftStatusComplete,
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

// runtimeOnlyWorkflow / runtimeOnlyScenario are a shape the retired graph
// simulator and the real runtime disagreed on: after an approval is DENIED,
// the runtime re-enters the loop (its `while` still sees the denied turn's
// tool_calls) and calls the LLM again, while the simulator ended the loop. The
// scenario supplies only the simulator's single call_llm event, so it PASSED
// on the simulator and must fail on the runtime with an exhausted-mocks
// mismatch — the proof that the RPC executes on the runtime.
const runtimeOnlyWorkflow = `name: denied-loop
apiVersion: "1.0"
entry: [agent_loop]
nodes:
  - id: agent_loop
    type: loop
    while: size(outputs.tool_calls) > 0
    inline:
      outputs:
        tool_calls: "{{nodes.call_llm.tool_calls}}"
      entry: [call_llm]
      nodes:
        - id: call_llm
          type: call_llm
          args:
            model: {tags: [fast]}
        - id: approval
          type: approval
          args:
            title: Approve?
        - id: execute_tools
          type: execute_tools
          args:
            tool_calls: "{{nodes.call_llm.tool_calls}}"
      edges:
        - from: call_llm
          cases:
            - to: approval
              condition: size(nodes.call_llm.tool_calls) > 0
        - from: approval
          cases:
            - to: execute_tools
              condition: nodes.approval.status == 'approved'
`

func TestScenarioService_RunScenario_ExecutesOnTheRuntime(t *testing.T) {
	service, repo, userID, _, ctx := setupTestScenarioService(t)
	now := time.Now().UTC()
	require.NoError(t, repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: userID, Name: "Denied Loop", Slug: "denied-loop",
		Definition: runtimeOnlyWorkflow, Status: db.WorkflowDraftStatusComplete,
		CreatedAt: now, UpdatedAt: now,
	}))

	resp, err := service.RunScenario(ctx, connect.NewRequest(&reliantv1.RunScenarioRequest{
		WorkflowSlug: "denied-loop",
		Scenario: &reliantv1.ScenarioDefinition{
			Name: "denied_once",
			Events: []*reliantv1.SimulatedEvent{
				{Node: "agent_loop.call_llm", OutputJson: `{"response_text":"rm it","tool_calls":[{"id":"c1","name":"shell","input":"{}"}]}`},
				{Node: "agent_loop.approval", OutputJson: `{"status":"denied"}`},
			},
			Expect: &reliantv1.ScenarioExpectation{Outcome: "completed"},
		},
	}))
	require.NoError(t, err)
	require.Equal(t, "failed", resp.Msg.Result.Status)
	require.Contains(t, strings.Join(resp.Msg.Result.Mismatches, "\n"),
		`scenario exhausted its mocks for node "agent_loop.call_llm"`,
		"the runtime re-enters the loop after a denial; the retired simulator did not")
}
