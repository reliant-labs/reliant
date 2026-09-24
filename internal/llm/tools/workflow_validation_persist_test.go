// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workflow-editing tools validate exactly as run start will — with a
// workflow loader, so ref'd children type-check — and validation ERRORS BLOCK
// THE WRITE. A stored draft is runnable (by chats and by `ref:`), and run start
// rejects the same errors, so persisting a broken draft only defers the
// failure to whoever runs it next. These tests pin the persisted row, not just
// the response text.

// The definition's `name:` must match the draft's own name, or an edit
// re-derives a different slug and every later lookup misses for that reason
// instead of the one under test.
func createValidPersistDraft(t *testing.T, repo db.Repository) *db.WorkflowDraft {
	t.Helper()
	now := time.Now()
	name := "persist-" + uuid.New().String()[:8]
	draft := &db.WorkflowDraft{
		ID:     uuid.New().String(),
		UserID: "test-user",
		Name:   name,
		Slug:   name,
		Definition: "name: " + name + `
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: mock
edges: []
`,
		IsValid:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(context.Background(), draft))
	return draft
}

// assertUnchanged reads the row back: the rejected write must not have
// touched it, so it is still the last valid definition and still usable.
func assertUnchanged(t *testing.T, repo db.Repository, before *db.WorkflowDraft) {
	t.Helper()
	stored, err := repo.GetWorkflowDraft(context.Background(), before.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, before.Definition, stored.Definition, "a rejected write must not persist the broken definition")
	assert.True(t, stored.IsValid)
	usable, err := repo.GetUsableWorkflowBySlug(context.Background(), "test-user", before.Slug)
	require.NoError(t, err)
	assert.NotNil(t, usable, "the last valid version must stay loadable by ref:")
}

func TestEditWorkflow_RejectsInvalidEdit(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewEditWorkflowTool(repo)
	ctx := createTestContext(t, "")

	t.Run("structurally broken workflow is not saved", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		inputJSON, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "entry: [agent]",
			NewString: "entry: [no-such-node]",
		})
		require.NoError(t, err)

		resp, err := tool.Run(ctx, ToolCall{ID: "edit-invalid", Name: EditWorkflowToolName, Input: string(inputJSON)})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "an edit with validation errors must be rejected: %s", resp.Content)
		assert.Contains(t, resp.Content, "NOT updated")
		assert.Contains(t, resp.Content, "no-such-node")
		assertUnchanged(t, repo, draft)
	})

	t.Run("unparseable YAML is not saved", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		inputJSON, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "entry: [agent]",
			NewString: "entry: [agent",
		})
		require.NoError(t, err)

		resp, err := tool.Run(ctx, ToolCall{ID: "edit-unparseable", Name: EditWorkflowToolName, Input: string(inputJSON)})
		require.NoError(t, err)
		assert.True(t, resp.IsError, "%s", resp.Content)
		assert.Contains(t, resp.Content, "invalid YAML syntax")
		assertUnchanged(t, repo, draft)
	})

	t.Run("a valid edit saves", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		inputJSON, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "model: mock",
			NewString: "model: other-mock",
		})
		require.NoError(t, err)
		resp, err := tool.Run(ctx, ToolCall{ID: "edit-ok", Name: EditWorkflowToolName, Input: string(inputJSON)})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "%s", resp.Content)

		stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
		require.NoError(t, err)
		assert.Contains(t, stored.Definition, "other-mock")
		assert.True(t, stored.IsValid)
	})
}

func TestWriteWorkflow_RejectsInvalidContent(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")
	draft := createValidPersistDraft(t, repo)

	invalidWorkflow := "name: " + draft.Name + "\nnodes:\n  - id: agent\n    type: call_llm\n"
	inputJSON, err := json.Marshal(WriteWorkflowParams{ID: draft.ID, Content: invalidWorkflow})
	require.NoError(t, err)

	resp, err := tool.Run(ctx, ToolCall{ID: "write-invalid", Name: WriteWorkflowToolName, Input: string(inputJSON)})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "%s", resp.Content)
	assert.Contains(t, resp.Content, "NOT saved")
	assertUnchanged(t, repo, draft)
}

// The tools validate WITH a workflow loader: a ref to a builtin types the
// child's declared outputs, so reading one it does not declare is an error
// here — not only later, at run start. builtin://structured-agent declares
// `response` and `completed`, and no `response_text`.
func TestWriteWorkflow_TypeChecksRefOutputs(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")
	draft := createValidPersistDraft(t, repo)

	content := "name: " + draft.Name + `
entry: [plan]
nodes:
  - id: plan
    type: workflow
    ref: builtin://structured-agent
  - id: report
    type: save_message
    args:
      role: assistant
      content: "{{nodes.plan.response_text}}"
edges:
  - {from: plan, default: [report]}
`
	inputJSON, err := json.Marshal(WriteWorkflowParams{ID: draft.ID, Content: content})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, ToolCall{ID: "write-ref", Name: WriteWorkflowToolName, Input: string(inputJSON)})
	require.NoError(t, err)
	assert.True(t, resp.IsError, "reading an output the ref does not declare must be rejected: %s", resp.Content)
	assert.Contains(t, resp.Content, "response_text")
	assertUnchanged(t, repo, draft)
}

// Warnings do not block a save but are always returned to the agent.
func TestWriteWorkflow_ReturnsWarnings(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")
	draft := createValidPersistDraft(t, repo)

	// A conditional run node read unguarded: the field exists either way (the
	// skip output zero-fills run fields), so it is a warning, not an error.
	content := "name: " + draft.Name + `
entry: [start]
inputs:
  lint: {type: boolean, default: false}
nodes:
  - {id: start, type: save_message, args: {role: user, content: go}}
  - id: lint
    type: run
    condition: "inputs.lint"
    command: "true"
  - id: report
    type: save_message
    args:
      role: assistant
      content: "lint exit: {{nodes.lint.exit_code}}"
edges:
  - {from: start, default: [lint]}
  - {from: lint, default: [report]}
`
	inputJSON, err := json.Marshal(WriteWorkflowParams{ID: draft.ID, Content: content})
	require.NoError(t, err)
	resp, err := tool.Run(ctx, ToolCall{ID: "write-warn", Name: WriteWorkflowToolName, Input: string(inputJSON)})
	require.NoError(t, err)
	require.False(t, resp.IsError, "%s", resp.Content)
	assert.Contains(t, resp.Content, "Warnings")
	assert.Contains(t, resp.Content, "exit_code")

	stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Definition, "lint exit")
}
