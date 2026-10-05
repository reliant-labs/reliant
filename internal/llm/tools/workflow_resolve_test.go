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

// createResolverDraft inserts a draft owned by "test-user", with unique
// names. Names and slugs are unique per call because drafts are unique on
// (user_id, slug) and these tests run in parallel against one database.
func createResolverDraft(t *testing.T, repo db.Repository) *db.WorkflowDraft {
	t.Helper()
	now := time.Now()
	unique := uuid.New().String()[:8]
	draft := &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     "test-user",
		Name:       "Resolver Draft " + unique,
		Slug:       "resolver-draft-" + unique,
		Definition: validWorkflowYAML,
		Status:     db.WorkflowDraftStatusComplete,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(context.Background(), draft))
	return draft
}

func TestResolveWorkflowDraft_ByUUID(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createResolverDraft(t, repo)
	ctx := createTestContext(t, "")

	got, err := resolveWorkflowDraft(ctx, repo, draft.ID)
	require.NoError(t, err)
	assert.Equal(t, draft.ID, got.ID)
}

func TestResolveWorkflowDraft_BySlug(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createResolverDraft(t, repo)
	ctx := createTestContext(t, "")

	got, err := resolveWorkflowDraft(ctx, repo, draft.Slug)
	require.NoError(t, err)
	assert.Equal(t, draft.ID, got.ID)
}

func TestResolveWorkflowDraft_ByName(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createResolverDraft(t, repo)
	ctx := createTestContext(t, "")

	// The display name is not the slug, so this can only be served by the
	// name lookup that runs after the slug lookup misses.
	got, err := resolveWorkflowDraft(ctx, repo, draft.Name)
	require.NoError(t, err)
	assert.Equal(t, draft.ID, got.ID)
}

func TestResolveWorkflowDraft_NotFound(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	t.Run("unknown name names both recovery tools", func(t *testing.T) {
		ctx := createTestContext(t, "")
		_, err := resolveWorkflowDraft(ctx, repo, "no-such-workflow")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create_workflow")
		assert.Contains(t, err.Error(), "list_workflows")
	})

	t.Run("unknown uuid names both recovery tools", func(t *testing.T) {
		ctx := createTestContext(t, "")
		_, err := resolveWorkflowDraft(ctx, repo, uuid.New().String())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create_workflow")
		assert.Contains(t, err.Error(), "list_workflows")
	})

	t.Run("empty id errors even in a chat context", func(t *testing.T) {
		chatID := "resolver-empty-" + uuid.New().String()
		createTestChat(t, repo, chatID)
		ctx := createTestContext(t, chatID)

		_, err := resolveWorkflowDraft(ctx, repo, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "`id` is required")
		assert.Contains(t, err.Error(), "create_workflow")
		assert.Contains(t, err.Error(), "list_workflows")
	})
}

// TestWorkflowTools_RequireID: every workflow/scenario tool that targets an
// existing workflow declares `id` as required, rejects an empty id, and does
// so even when a draft is still bound to the chat via the retired chat_id
// column — nothing binds a chat to a workflow any more.
func TestWorkflowTools_RequireID(t *testing.T) {
	t.Parallel()
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	chatID := "require-id-" + uuid.New().String()
	createTestChat(t, repo, chatID)
	// A draft the chat is (legacy-)bound to. An empty id must not find it.
	bound := createResolverDraft(t, repo)
	_, err := rawDB.Exec(`UPDATE workflow_drafts SET chat_id = $1 WHERE id = $2`, chatID, bound.ID)
	require.NoError(t, err)

	tools := map[string]Tool{
		GetWorkflowToolName:   NewGetWorkflowTool(repo),
		"edit_workflow":       NewEditWorkflowTool(repo),
		"write_workflow":      NewWriteWorkflowTool(repo),
		ListScenariosToolName: NewListScenariosTool(repo),
		"view_scenario":       NewViewScenarioTool(repo),
		"edit_scenario":       NewEditScenarioTool(repo),
		"write_scenario":      NewWriteScenarioTool(repo, nil),
		"delete_scenario":     NewDeleteScenarioTool(repo),
		"run_scenario":        NewRunScenarioTool(repo, nil),
	}
	require.Len(t, tools, 9)
	extra := map[string]string{
		"edit_workflow":   `"old_string":"a","new_string":"b"`,
		"write_workflow":  `"content":"name: x"`,
		"view_scenario":   `"name":"s"`,
		"edit_scenario":   `"name":"s","old_string":"a","new_string":"b"`,
		"write_scenario":  `"name":"s","content":"name: s"`,
		"delete_scenario": `"name":"s"`,
		"run_scenario":    `"name":"s"`,
	}
	for name, tool := range tools {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, tool.ParamSchema().Required, "id", "%s must declare id as required", name)

			input := `{"id":""`
			if e := extra[name]; e != "" {
				input += "," + e
			}
			input += "}"
			resp, err := tool.Run(createTestContext(t, chatID), ToolCall{ID: "req-id", Name: name, Input: input})
			require.NoError(t, err)
			require.True(t, resp.IsError, "%s must reject an empty id: %s", name, resp.Content)
			assert.Contains(t, resp.Content, "`id` is required")
		})
	}
}

func TestResolveWorkflowDraft_OtherUsersUUIDNotFound(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now()
	unique := uuid.New().String()[:8]
	foreign := &db.WorkflowDraft{
		ID: uuid.New().String(), UserID: "someone-else", Name: "Foreign " + unique,
		Slug: "foreign-" + unique, Definition: validWorkflowYAML,
		Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateWorkflowDraft(context.Background(), foreign))

	_, err := resolveWorkflowDraft(createTestContext(t, ""), repo, foreign.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workflow not found")
}

func TestCreateWorkflow_DuplicateSlugIsClearToolError(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := createTestContext(t, "")

	name := "dup-" + uuid.New().String()[:8]
	content := "name: " + name + "\nentry: [run]\nnodes:\n  - id: run\n    type: run\n    command: echo hi\n"
	input, _ := json.Marshal(CreateWorkflowParams{Content: &content})
	call := ToolCall{ID: "c", Name: "create_workflow", Input: string(input)}

	first, err := NewCreateWorkflowTool(repo).Run(ctx, call)
	require.NoError(t, err)
	require.False(t, first.IsError, first.Content)

	start := time.Now()
	second, err := NewCreateWorkflowTool(repo).Run(ctx, call)
	require.NoError(t, err)
	require.True(t, second.IsError)
	assert.Contains(t, second.Content, name)
	assert.Contains(t, second.Content, "list_workflows")
	assert.NotContains(t, second.Content, "SQLSTATE")
	assert.Less(t, time.Since(start), 700*time.Millisecond)
}

// TestCreateWorkflow_ReturnedIDWorksWithoutChatBinding: create_workflow's id is
// the only handle a chat needs.
func TestCreateWorkflow_ReturnedIDWorksWithoutChatBinding(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "create-then-get-" + uuid.New().String()
	createTestChat(t, repo, chatID)
	ctx := createTestContext(t, chatID)

	name := "unbound-" + uuid.New().String()[:8]
	content := "name: " + name + "\ndescription: before\nentry: [run]\nnodes:\n  - id: run\n    type: run\n    command: echo hi\n"
	createInput, _ := json.Marshal(CreateWorkflowParams{Content: &content})
	resp, err := NewCreateWorkflowTool(repo).Run(ctx, ToolCall{ID: "c", Name: "create_workflow", Input: string(createInput)})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	var created CreateWorkflowResult
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &created))
	assert.Contains(t, resp.Content, "Pass this `id`")

	input, _ := json.Marshal(GetWorkflowParams{ID: created.ID})
	got, err := NewGetWorkflowTool(repo).Run(ctx, ToolCall{ID: "g", Name: GetWorkflowToolName, Input: string(input)})
	require.NoError(t, err)
	assert.False(t, got.IsError, got.Content)
	assert.Contains(t, got.Content, created.ID)

	edit, _ := json.Marshal(EditWorkflowParams{ID: created.ID, OldString: "description: before", NewString: "description: after"})
	ed, err := NewEditWorkflowTool(repo).Run(ctx, ToolCall{ID: "e", Name: "edit_workflow", Input: string(edit)})
	require.NoError(t, err)
	require.False(t, ed.IsError, ed.Content)

	stored, err := repo.GetWorkflowDraft(ctx, created.ID)
	require.NoError(t, err)
	assert.Contains(t, stored.Definition, "description: after", "the edit must land on the draft named by id")
}
