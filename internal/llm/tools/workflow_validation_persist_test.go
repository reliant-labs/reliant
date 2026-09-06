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

// The `is_valid` column is not cosmetic: ListUsableWorkflowsByUser and
// GetUsableWorkflowBySlug both filter on `is_valid = 1`, so a draft that is
// saved as valid while being broken stays loadable by `ref:` at runtime and
// renders with a ✓ in list_workflows. These tests pin the PERSISTED row rather
// than the response text, because the response text was always correct — it was
// only the database that lied.

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

// assertPersistedInvalid reads the row back and checks both halves of the
// corruption: the flag AND the stored errors.
func assertPersistedInvalid(t *testing.T, repo db.Repository, id string) {
	t.Helper()
	stored, err := repo.GetWorkflowDraft(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, stored)

	assert.False(t, stored.IsValid, "draft was edited into a broken state but persisted is_valid=true")
	require.NotNil(t, stored.ValidationErrors, "validation_errors should be persisted for an invalid draft")

	var errs []string
	require.NoError(t, json.Unmarshal([]byte(*stored.ValidationErrors), &errs),
		"validation_errors should be a JSON array of strings, matching what the gRPC save path writes")
	require.NotEmpty(t, errs)
	assert.NotEmpty(t, errs[0])
}

func TestEditWorkflow_PersistsInvalidState(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewEditWorkflowTool(repo)
	ctx := createTestContext(t, "")

	t.Run("structurally broken workflow marks the row invalid", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)

		// Parses as YAML, but the entry node does not exist — the v2 parser
		// rejects it, so this exercises the ordinary save path.
		inputJSON, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "entry: [agent]",
			NewString: "entry: [no-such-node]",
		})
		require.NoError(t, err)

		resp, err := tool.Run(ctx, ToolCall{ID: "edit-invalid", Name: EditWorkflowToolName, Input: string(inputJSON)})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "edit still saves: %s", resp.Content)
		assert.Contains(t, resp.Content, "validation errors")

		assertPersistedInvalid(t, repo, draft.ID)
	})

	t.Run("unparseable YAML marks the row invalid", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)

		// An unterminated flow sequence makes yaml.Unmarshal itself fail, which
		// takes the name-extraction failure branch — a second save site that
		// also used to hardcode valid.
		inputJSON, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "entry: [agent]",
			NewString: "entry: [agent",
		})
		require.NoError(t, err)

		resp, err := tool.Run(ctx, ToolCall{ID: "edit-unparseable", Name: EditWorkflowToolName, Input: string(inputJSON)})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "edit still saves: %s", resp.Content)

		assertPersistedInvalid(t, repo, draft.ID)
	})

	t.Run("repairing a workflow clears the invalid state", func(t *testing.T) {
		draft := createValidPersistDraft(t, repo)

		breakIt, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "entry: [agent]",
			NewString: "entry: [no-such-node]",
		})
		require.NoError(t, err)
		_, err = tool.Run(ctx, ToolCall{ID: "edit-break", Name: EditWorkflowToolName, Input: string(breakIt)})
		require.NoError(t, err)
		assertPersistedInvalid(t, repo, draft.ID)

		fixIt, err := json.Marshal(EditWorkflowParams{
			ID:        draft.ID,
			OldString: "entry: [no-such-node]",
			NewString: "entry: [agent]",
		})
		require.NoError(t, err)
		resp, err := tool.Run(ctx, ToolCall{ID: "edit-fix", Name: EditWorkflowToolName, Input: string(fixIt)})
		require.NoError(t, err)
		assert.False(t, resp.IsError, "%s", resp.Content)

		stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
		require.NoError(t, err)
		assert.True(t, stored.IsValid, "a repaired workflow must become usable again")
		assert.Nil(t, stored.ValidationErrors, "stale validation errors must be cleared on repair")
	})
}

func TestWriteWorkflow_PersistsInvalidState(t *testing.T) {
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
	assert.False(t, resp.IsError, "write still saves: %s", resp.Content)
	assert.Contains(t, resp.Content, "validation errors")

	assertPersistedInvalid(t, repo, draft.ID)
}

// TestInvalidDraftIsNotRuntimeLoadable is the consequence the flag exists for:
// a broken workflow must drop out of the usable set that `ref:` resolution and
// the agent selector read from.
func TestInvalidDraftIsNotRuntimeLoadable(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createValidPersistDraft(t, repo)
	ctx := createTestContext(t, "")

	usable, err := repo.GetUsableWorkflowBySlug(context.Background(), "test-user", draft.Slug)
	require.NoError(t, err)
	require.NotNil(t, usable, "a valid draft should be loadable before the edit")

	inputJSON, err := json.Marshal(EditWorkflowParams{
		ID:        draft.ID,
		OldString: "entry: [agent]",
		NewString: "entry: [no-such-node]",
	})
	require.NoError(t, err)
	_, err = NewEditWorkflowTool(repo).Run(ctx, ToolCall{ID: "edit-runtime", Name: EditWorkflowToolName, Input: string(inputJSON)})
	require.NoError(t, err)

	// The edit leaves the name (and therefore the slug) alone, so a hit here
	// can only mean the is_valid filter let a broken workflow through.
	broken, err := repo.GetUsableWorkflowBySlug(context.Background(), "test-user", draft.Slug)
	require.NoError(t, err)
	assert.Nil(t, broken, "a broken workflow must no longer be loadable at runtime")
}
