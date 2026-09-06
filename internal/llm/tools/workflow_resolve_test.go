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

// createResolverDraft inserts a draft owned by "test-user", optionally bound to
// a chat. Names and slugs are unique per call because drafts are unique on
// (user_id, slug) and these tests run in parallel against one database.
func createResolverDraft(t *testing.T, repo db.Repository, chatID *string) *db.WorkflowDraft {
	t.Helper()
	now := time.Now()
	unique := uuid.New().String()[:8]
	draft := &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     "test-user",
		Name:       "Resolver Draft " + unique,
		Slug:       "resolver-draft-" + unique,
		Definition: validWorkflowYAML,
		IsValid:    true,
		ChatID:     chatID,
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

	draft := createResolverDraft(t, repo, nil)
	ctx := createTestContext(t, "")

	got, err := resolveWorkflowDraft(ctx, repo, draft.ID)
	require.NoError(t, err)
	assert.Equal(t, draft.ID, got.ID)
}

func TestResolveWorkflowDraft_BySlug(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createResolverDraft(t, repo, nil)
	ctx := createTestContext(t, "")

	got, err := resolveWorkflowDraft(ctx, repo, draft.Slug)
	require.NoError(t, err)
	assert.Equal(t, draft.ID, got.ID)
}

func TestResolveWorkflowDraft_ByName(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	draft := createResolverDraft(t, repo, nil)
	ctx := createTestContext(t, "")

	// The display name is not the slug, so this can only be served by the
	// name lookup that runs after the slug lookup misses.
	got, err := resolveWorkflowDraft(ctx, repo, draft.Name)
	require.NoError(t, err)
	assert.Equal(t, draft.ID, got.ID)
}

// TestResolveWorkflowDraft_FromChatContext is the branch that replaces the
// system message the frontend used to inject on every turn: with no id at all,
// the tool still finds the workflow this chat is editing.
func TestResolveWorkflowDraft_FromChatContext(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "resolver-chat-" + uuid.New().String()
	createTestChat(t, repo, chatID)
	draft := createResolverDraft(t, repo, &chatID)

	ctx := createTestContext(t, chatID)

	got, err := resolveWorkflowDraft(ctx, repo, "")
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

	t.Run("empty id with a chat that has no draft", func(t *testing.T) {
		chatID := "resolver-empty-" + uuid.New().String()
		createTestChat(t, repo, chatID)
		ctx := createTestContext(t, chatID)

		_, err := resolveWorkflowDraft(ctx, repo, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create_workflow")
		assert.Contains(t, err.Error(), "list_workflows")
	})

	t.Run("empty id with no chat context at all", func(t *testing.T) {
		ctx := createTestContext(t, "")
		_, err := resolveWorkflowDraft(ctx, repo, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "create_workflow")
	})
}

// TestGetWorkflow_DefaultsToChatWorkflow pins the end-to-end effect through a
// real tool: no id in the arguments, and the right workflow still comes back.
func TestGetWorkflow_DefaultsToChatWorkflow(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "get-workflow-chat-" + uuid.New().String()
	createTestChat(t, repo, chatID)
	draft := createResolverDraft(t, repo, &chatID)

	tool := NewGetWorkflowTool(repo)
	resp, err := tool.Run(createTestContext(t, chatID), ToolCall{
		ID:    "test-get-default",
		Name:  GetWorkflowToolName,
		Input: "{}",
	})

	require.NoError(t, err)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
	assert.Contains(t, resp.Content, draft.ID)
}

// TestWriteWorkflow_DefaultsToChatWorkflow covers a mutating tool taking the
// same path, since write_workflow previously rejected a missing id outright.
func TestWriteWorkflow_DefaultsToChatWorkflow(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "write-workflow-chat-" + uuid.New().String()
	createTestChat(t, repo, chatID)
	draft := createResolverDraft(t, repo, &chatID)

	unique := uuid.New().String()[:8]
	content := `name: chat-default-` + unique + `
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: mock
edges: []
`
	inputJSON, err := json.Marshal(WriteWorkflowParams{Content: content})
	require.NoError(t, err)

	tool := NewWriteWorkflowTool(repo)
	resp, err := tool.Run(createTestContext(t, chatID), ToolCall{
		ID:    "test-write-default",
		Name:  WriteWorkflowToolName,
		Input: string(inputJSON),
	})

	require.NoError(t, err)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)

	var result WriteWorkflowResult
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &result))
	assert.Equal(t, draft.ID, result.ID, "should have written the chat's own draft")

	stored, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
	require.NoError(t, err)
	assert.Equal(t, content, stored.Definition)
}

// TestListScenarios_DefaultsToChatWorkflow makes the list_scenarios prose
// ("no parameters needed") true in the schema as well.
func TestListScenarios_DefaultsToChatWorkflow(t *testing.T) {
	t.Parallel()
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	chatID := "list-scenarios-chat-" + uuid.New().String()
	createTestChat(t, repo, chatID)
	draft := createResolverDraft(t, repo, &chatID)

	tool := NewListScenariosTool(repo)
	resp, err := tool.Run(createTestContext(t, chatID), ToolCall{
		ID:    "test-list-default",
		Name:  ListScenariosToolName,
		Input: "{}",
	})

	require.NoError(t, err)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
	// No scenarios exist yet, but the workflow resolved — which is the point.
	assert.Contains(t, resp.Content, "No scenarios found")
	_ = draft
}
