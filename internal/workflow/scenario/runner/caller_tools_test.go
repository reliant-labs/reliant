// Copyright (c) 2025 Reliant Labs
package runner

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deniedLoopWorkflow and deniedOnceScenario are a shape the retired graph
// simulator and the real runtime disagreed on. After an approval is DENIED the
// runtime re-enters the loop — its `while` still sees the denied turn's
// tool_calls — and calls the LLM again; the simulator ended the loop. The
// scenario supplies only the simulator's single call_llm event, so it PASSED
// on the simulator and fails on the runtime with an exhausted-mocks mismatch.
// A caller that reports that mismatch is executing on the runtime.
const deniedLoopWorkflow = `name: denied-loop
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

const deniedOnceScenario = `name: denied_once
events:
  - node: agent_loop.call_llm
    output:
      response_text: rm it
      tool_calls:
        - {id: c1, name: shell, input: "{}"}
  - node: agent_loop.approval
    output: {status: denied}
expect:
  outcome: completed
`

// The run_scenario agent tool executes on the runner: it is built exactly as
// the server wires it (ToolsOptions.ScenarioRunner = RunScenario), run through
// its JSON boundary, and reports the runtime's verdict — and persists it.
func TestRunScenarioTool_ExecutesOnTheRuntime(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	unique := uuid.NewString()[:8]
	draft := &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user",
		Name: "Denied Loop " + unique, Slug: "denied-loop-" + unique,
		Definition: deniedLoopWorkflow, Status: db.WorkflowDraftStatusComplete,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))
	stored := &db.WorkflowScenario{
		ID:              uuid.NewString(),
		WorkflowDraftID: sql.NullString{String: draft.ID, Valid: true},
		UserID:          "test-user",
		Name:            "denied_once",
		Events:          deniedOnceScenario,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, repo.CreateWorkflowScenario(ctx, stored))

	factory := tools.NewToolsFactory(&tools.ToolsOptions{Repo: repo, ScenarioRunner: RunScenario})
	resp, err := factory.RunScenario().Run(rctx.NewToolContext(ctx, "", "", nil, nil), tools.ToolCall{
		ID:    "c1",
		Input: `{"id":"` + draft.ID + `","name":"denied_once"}`,
	})
	require.NoError(t, err)
	assert.Contains(t, resp.Content, `scenario exhausted its mocks for node "agent_loop.call_llm"`,
		"the runtime re-enters the loop after a denial; the retired simulator did not")

	after, err := repo.GetWorkflowScenario(ctx, stored.ID)
	require.NoError(t, err)
	assert.Equal(t, "failed", after.LastRunStatus.String, "the tool persists the runtime's verdict")
}

// Without an injected runner (the daemon runtime), the tool reports that
// execution is unavailable instead of falling back to anything.
func TestRunScenarioTool_WithoutRunnerIsUnavailable(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	now := time.Now().UTC()
	unique := uuid.NewString()[:8]
	draft := &db.WorkflowDraft{
		ID: uuid.NewString(), UserID: "test-user",
		Name: "No Runner " + unique, Slug: "no-runner-" + unique,
		Definition: deniedLoopWorkflow, Status: db.WorkflowDraftStatusComplete,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(ctx, draft))

	resp, err := tools.NewToolsFactory(&tools.ToolsOptions{Repo: repo}).RunScenario().Run(
		rctx.NewToolContext(ctx, "", "", nil, nil),
		tools.ToolCall{ID: "c1", Input: `{"id":"` + draft.ID + `","name":"x"}`})
	require.NoError(t, err)
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "not available")
}
