// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workflow-editing tools validate exactly as run start will — with a
// workflow loader, so ref'd children type-check — and follow the draft
// lifecycle (specs/workflow-draft-lifecycle.md):
//   - a DRAFT stores even with validation errors (agents iterate), and is never
//     runnable;
//   - a COMPLETE workflow must stay valid: a write with errors is rejected and
//     the row is left as it was, unless the agent passes complete:false;
//   - complete:true is the gate to becoming runnable;
//   - YAML that does not parse is rejected either way.
//
// Every response carries the resulting status and every current finding.
// These tests pin the persisted row, not just the response text.

// The definition's `name:` must match the draft's own name, or an edit
// re-derives a different slug and every later lookup misses for that reason
// instead of the one under test.
func createPersistDraft(t *testing.T, repo db.Repository, status db.WorkflowDraftStatus) *db.WorkflowDraft {
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
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(context.Background(), draft))
	return draft
}

func createValidPersistDraft(t *testing.T, repo db.Repository) *db.WorkflowDraft {
	return createPersistDraft(t, repo, db.WorkflowDraftStatusComplete)
}

func runWorkflowTool(t *testing.T, tool Tool, name string, params any) ToolResponse {
	t.Helper()
	inputJSON, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(createTestContext(t, ""), ToolCall{ID: "call-" + uuid.NewString()[:8], Name: name, Input: string(inputJSON)})
	require.NoError(t, err)
	return resp
}

func boolPtr(b bool) *bool { return &b }

// assertUnchanged reads the row back: the rejected write must not have
// touched it, so it is still the last valid definition and still runnable.
func assertUnchanged(t *testing.T, repo db.Repository, before *db.WorkflowDraft) {
	t.Helper()
	stored, err := repo.GetWorkflowDraft(context.Background(), before.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, before.Definition, stored.Definition, "a rejected write must not persist the broken definition")
	assert.Equal(t, db.WorkflowDraftStatusComplete, stored.Status)
	usable, err := repo.GetUsableWorkflowBySlug(context.Background(), "test-user", before.Slug)
	require.NoError(t, err)
	assert.NotNil(t, usable, "the last valid version must stay loadable by ref:")
}

func TestEditWorkflow_CompleteWorkflowRejectsInvalidEdit(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewEditWorkflowTool(repo)

	t.Run("an edit that breaks a complete workflow is not saved", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		resp := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
			ID: draft.ID, OldString: "entry: [agent]", NewString: "entry: [no-such-node]",
		})
		assert.True(t, resp.IsError, "an edit with validation errors must be rejected for a complete workflow: %s", resp.Content)
		assert.Contains(t, resp.Content, "NOT updated")
		assert.Contains(t, resp.Content, "no-such-node")
		assert.Contains(t, resp.Content, "complete: false", "the rejection names the way to keep iterating")
		assertUnchanged(t, repo, draft)
	})

	t.Run("unparseable YAML is not saved", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		resp := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
			ID: draft.ID, OldString: "entry: [agent]", NewString: "entry: [agent",
		})
		assert.True(t, resp.IsError, "%s", resp.Content)
		assert.Contains(t, resp.Content, "invalid YAML syntax")
		assertUnchanged(t, repo, draft)
	})

	t.Run("a valid edit saves and stays complete", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		resp := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
			ID: draft.ID, OldString: "model: mock", NewString: "model: other-mock",
		})
		assert.False(t, resp.IsError, "%s", resp.Content)
		assert.Contains(t, resp.Content, "Status: complete")

		stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
		require.NoError(t, err)
		assert.Contains(t, stored.Definition, "other-mock")
		assert.Equal(t, db.WorkflowDraftStatusComplete, stored.Status)
	})

	t.Run("complete:false saves the invalid edit as a draft", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)
		resp := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
			ID: draft.ID, OldString: "entry: [agent]", NewString: "entry: [no-such-node]", Complete: boolPtr(false),
		})
		require.False(t, resp.IsError, "%s", resp.Content)
		assert.Contains(t, resp.Content, "Status: draft")
		assert.Contains(t, resp.Content, "no-such-node", "the response carries the current errors")

		stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
		require.NoError(t, err)
		assert.Contains(t, stored.Definition, "no-such-node")
		assert.Equal(t, db.WorkflowDraftStatusDraft, stored.Status)
	})
}

// A draft stores an invalid edit by default, reports the errors, and is not
// runnable. complete:true then gates on validation.
func TestEditWorkflow_DraftIteratesThenCompleteGates(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewEditWorkflowTool(repo)
	draft := createPersistDraft(t, repo, db.WorkflowDraftStatusDraft)

	broken := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
		ID: draft.ID, OldString: "entry: [agent]", NewString: "entry: [no-such-node]",
	})
	require.False(t, broken.IsError, "a draft stores invalid work in progress: %s", broken.Content)
	assert.Contains(t, broken.Content, "Status: draft")
	assert.Contains(t, broken.Content, "no-such-node")
	stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Definition, "no-such-node")
	assert.Equal(t, db.WorkflowDraftStatusDraft, stored.Status)

	usable, err := repo.GetUsableWorkflowBySlug(context.Background(), "test-user", draft.Slug)
	assert.Nil(t, usable, "a draft is never runnable")
	var notRunnable *db.WorkflowDraftNotRunnableError
	assert.True(t, errors.As(err, &notRunnable), "got %v", err)

	gated := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
		ID: draft.ID, OldString: "model: mock", NewString: "model: other-mock", Complete: boolPtr(true),
	})
	assert.True(t, gated.IsError, "complete:true on an invalid workflow must be rejected: %s", gated.Content)
	stored, err = repo.GetWorkflowDraft(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Equal(t, db.WorkflowDraftStatusDraft, stored.Status)
	assert.NotContains(t, stored.Definition, "other-mock")

	done := runWorkflowTool(t, tool, EditWorkflowToolName, EditWorkflowParams{
		ID: draft.ID, OldString: "entry: [no-such-node]", NewString: "entry: [agent]", Complete: boolPtr(true),
	})
	require.False(t, done.IsError, "%s", done.Content)
	assert.Contains(t, done.Content, "Status: complete")
	usable, err = repo.GetUsableWorkflowBySlug(context.Background(), "test-user", draft.Slug)
	require.NoError(t, err)
	assert.NotNil(t, usable)
}

func TestWriteWorkflow_CompleteWorkflowRejectsInvalidContent(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewWriteWorkflowTool(repo)
	draft := createValidPersistDraft(t, repo)

	invalidWorkflow := "name: " + draft.Name + "\nnodes:\n  - id: agent\n    type: call_llm\n"
	resp := runWorkflowTool(t, tool, WriteWorkflowToolName, WriteWorkflowParams{ID: draft.ID, Content: invalidWorkflow})
	assert.True(t, resp.IsError, "%s", resp.Content)
	assert.Contains(t, resp.Content, "NOT saved")
	assertUnchanged(t, repo, draft)
}

// create_workflow defaults to a draft: invalid content is stored and the
// response reports status + errors. complete:true gates.
func TestCreateWorkflow_DefaultsToDraftAndCompleteGates(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewCreateWorkflowTool(repo)

	name := "created-" + uuid.NewString()[:8]
	invalid := "name: " + name + "\nnodes:\n  - id: agent\n    type: call_llm\n"

	rejected := runWorkflowTool(t, tool, CreateWorkflowToolName, CreateWorkflowParams{Content: &invalid, Complete: boolPtr(true)})
	assert.True(t, rejected.IsError, "complete:true on invalid content must be rejected: %s", rejected.Content)
	existing, err := repo.GetWorkflowDraftBySlug(context.Background(), "test-user", name)
	require.NoError(t, err)
	assert.Nil(t, existing, "a rejected create stores nothing")

	created := runWorkflowTool(t, tool, CreateWorkflowToolName, CreateWorkflowParams{Content: &invalid})
	require.False(t, created.IsError, "a draft create stores invalid content: %s", created.Content)
	assert.Contains(t, created.Content, "Status: draft")
	assert.Contains(t, created.Content, "Errors:")
	var result CreateWorkflowResult
	require.NoError(t, json.Unmarshal([]byte(created.Metadata), &result))
	assert.Equal(t, "draft", result.Status)
	stored, err := repo.GetWorkflowDraft(context.Background(), result.ID)
	require.NoError(t, err)
	assert.Equal(t, db.WorkflowDraftStatusDraft, stored.Status)
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
	resp := runWorkflowTool(t, tool, WriteWorkflowToolName, WriteWorkflowParams{ID: draft.ID, Content: content})
	assert.True(t, resp.IsError, "reading an output the ref does not declare must be rejected: %s", resp.Content)
	assert.Contains(t, resp.Content, "response_text")
	assertUnchanged(t, repo, draft)
}

// A ref to one of the user's DRAFTS is a validation error, exactly as at run
// start — so a workflow that refs a draft cannot be marked complete. A
// self-spawn still resolves (to the content being validated).
func TestWriteWorkflow_RefToDraftIsAnError(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewWriteWorkflowTool(repo)
	child := createPersistDraft(t, repo, db.WorkflowDraftStatusDraft)
	parent := createPersistDraft(t, repo, db.WorkflowDraftStatusDraft)

	content := "name: " + parent.Name + `
entry: [child]
nodes:
  - id: child
    type: workflow
    ref: ` + child.Slug + `
`
	resp := runWorkflowTool(t, tool, WriteWorkflowToolName, WriteWorkflowParams{ID: parent.ID, Content: content, Complete: boolPtr(true)})
	assert.True(t, resp.IsError, "a parent that refs a draft must not become complete: %s", resp.Content)
	assert.Contains(t, resp.Content, "is a draft")
}

// Warnings do not block a save but are always returned to the agent.
func TestWriteWorkflow_ReturnsWarnings(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	tool := NewWriteWorkflowTool(repo)
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
	resp := runWorkflowTool(t, tool, WriteWorkflowToolName, WriteWorkflowParams{ID: draft.ID, Content: content})
	require.False(t, resp.IsError, "%s", resp.Content)
	assert.Contains(t, resp.Content, "Warnings")
	assert.Contains(t, resp.Content, "exit_code")

	stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Definition, "lint exit")
}
