// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDB returns this package's isolated, reset-per-test database. It
// already seeds the shared "test-project" (see db.SetupTestDB), so tests can
// reference that project ID without creating it themselves.
func setupTestDB(t *testing.T) (db.Repository, func()) {
	t.Helper()
	return db.SetupTestDB(t)
}

// createTestContext creates a tool context with the test user ID
// Use empty string for chatID to avoid FK constraint issues
func createTestContext(t *testing.T, chatID string) *rctx.ToolContext {
	t.Helper()
	// Set user ID in context using the context key
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	// Use chatID as thread ID for test convenience (plan tools scope by thread)
	return rctx.NewToolContext(ctx, chatID, chatID, nil, nil)
}

// createTestChat creates a chat in the database for tests that need chat association
func createTestChat(t *testing.T, repo db.Repository, chatID string) {
	t.Helper()
	workflowName := "test-workflow"
	now := time.Now()
	err := repo.CreateChat(context.Background(), &db.Chat{
		ID:           chatID,
		Title:        "Test Chat",
		ProjectID:    "test-project",
		UserID:       "test-user",
		WorkflowName: &workflowName,
		CreatedAt:    now,
		UpdatedAt:    now,
		LastActive:   now,
	})
	require.NoError(t, err)
}

// validWorkflowYAML is a minimal valid workflow YAML for testing
const validWorkflowYAML = `name: test-workflow
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model: mock
edges: []
`

// =============================================================================
// WriteWorkflow Tests
// =============================================================================

func TestWriteWorkflow_RequiresID(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")

	t.Run("writes workflow content to draft", func(t *testing.T) {
		// First create a draft
		now := time.Now()
		uniqueID := uuid.New().String()[:8]
		draft := &db.WorkflowDraft{
			ID:        uuid.New().String(),
			UserID:    "test-user",
			Name:      "placeholder-" + uniqueID,
			Slug:      "placeholder-" + uniqueID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := repo.CreateWorkflowDraft(context.Background(), draft)
		require.NoError(t, err)

		params := WriteWorkflowParams{
			ID:      draft.ID,
			Content: validWorkflowYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-1",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
		assert.Contains(t, resp.Content, "updated successfully")
		assert.Contains(t, resp.Content, "test-workflow")

		// Verify metadata
		var result WriteWorkflowResult
		err = json.Unmarshal([]byte(resp.Metadata), &result)
		require.NoError(t, err)
		assert.False(t, result.Created, "Should indicate created=false for update")
		assert.Equal(t, "test-workflow", result.Name)
		assert.NotEmpty(t, result.ID)
		assert.Equal(t, "test-workflow", result.Slug)
	})

	t.Run("writes workflow with name override", func(t *testing.T) {
		// First create a draft
		now := time.Now()
		uniqueID := uuid.New().String()[:8]
		draft := &db.WorkflowDraft{
			ID:        uuid.New().String(),
			UserID:    "test-user",
			Name:      "placeholder-" + uniqueID,
			Slug:      "placeholder-" + uniqueID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := repo.CreateWorkflowDraft(context.Background(), draft)
		require.NoError(t, err)

		name := "My Custom Name"
		params := WriteWorkflowParams{
			ID:      draft.ID,
			Name:    &name,
			Content: validWorkflowYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-2",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
		assert.Contains(t, resp.Content, "My Custom Name")

		var result WriteWorkflowResult
		err = json.Unmarshal([]byte(resp.Metadata), &result)
		require.NoError(t, err)
		assert.Equal(t, "My Custom Name", result.Name)
		assert.Equal(t, "my-custom-name", result.Slug)
	})

	t.Run("associates workflow with chat ID", func(t *testing.T) {
		chatID := "test-chat-" + uuid.New().String()
		// Create the chat first so FK constraint is satisfied
		createTestChat(t, repo, chatID)
		ctxWithChat := createTestContext(t, chatID)

		// First create a draft
		now := time.Now()
		uniqueID := uuid.New().String()[:8]
		draft := &db.WorkflowDraft{
			ID:        uuid.New().String(),
			UserID:    "test-user",
			Name:      "placeholder-" + uniqueID,
			Slug:      "placeholder-" + uniqueID,
			ChatID:    &chatID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := repo.CreateWorkflowDraft(context.Background(), draft)
		require.NoError(t, err)

		// Use a unique workflow name to avoid conflicts with other tests
		workflowYAML := `name: chat-workflow-` + uniqueID + `
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model:
        tags: [flagship]
edges: []
`
		params := WriteWorkflowParams{
			ID:      draft.ID,
			Content: workflowYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctxWithChat, ToolCall{
			ID:    "test-3",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)

		var result WriteWorkflowResult
		err = json.Unmarshal([]byte(resp.Metadata), &result)
		require.NoError(t, err)

		// Verify the draft was associated with the chat
		updatedDraft, err := repo.GetWorkflowDraft(context.Background(), result.ID)
		require.NoError(t, err)
		require.NotNil(t, updatedDraft)
		assert.Equal(t, &chatID, updatedDraft.ChatID)
	})
}

func TestWriteWorkflow_UpdateExisting(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")

	// First create a draft
	// Note: slug must match what generateSlugFromName would produce
	now := time.Now()
	existingDraft := &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     "test-user",
		Name:       "Original Name",
		Slug:       "original-name",
		Definition: validWorkflowYAML,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	err := repo.CreateWorkflowDraft(context.Background(), existingDraft)
	require.NoError(t, err)

	t.Run("updates existing workflow with ID", func(t *testing.T) {
		updatedYAML := `name: updated-workflow
entry: [agent]
nodes:
  - id: agent
    type: call_llm
    args:
      model:
        tags: [flagship]
edges: []
`
		params := WriteWorkflowParams{
			ID:      existingDraft.ID,
			Content: updatedYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-update",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
		assert.Contains(t, resp.Content, "updated successfully")

		var result WriteWorkflowResult
		err = json.Unmarshal([]byte(resp.Metadata), &result)
		require.NoError(t, err)
		assert.False(t, result.Created, "Should indicate created=false for update")
		assert.Equal(t, existingDraft.ID, result.ID)
		assert.Equal(t, "updated-workflow", result.Name)
		// Slug is regenerated from the new name
		assert.Equal(t, "updated-workflow", result.Slug)
	})

	t.Run("returns error for non-existent ID", func(t *testing.T) {
		nonExistentID := uuid.New().String()
		params := WriteWorkflowParams{
			ID:      nonExistentID,
			Content: validWorkflowYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-not-found",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err) // Tool returns error in response, not as Go error
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "not found")
	})
}

func TestWriteWorkflow_ConflictDetection(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")

	// Create a draft
	now := time.Now()
	draft := &db.WorkflowDraft{
		ID:         uuid.New().String(),
		UserID:     "test-user",
		Name:       "Conflict Test",
		Slug:       "conflict-test-12345678",
		Definition: validWorkflowYAML,
		IsValid:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	err := repo.CreateWorkflowDraft(context.Background(), draft)
	require.NoError(t, err)

	t.Run("detects conflict when workflow was modified", func(t *testing.T) {
		// Fetch fresh from DB to get the actual stored version
		freshDraft, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
		require.NoError(t, err)
		require.NotNil(t, freshDraft)

		// Simulate fetching an older version (current version - 1)
		oldVersion := freshDraft.Version - 1

		params := WriteWorkflowParams{
			ID:              draft.ID,
			Content:         validWorkflowYAML,
			ExpectedVersion: &oldVersion,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-conflict",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.True(t, resp.IsError, "Expected error for version mismatch, got: %s", resp.Content)
		assert.Contains(t, resp.Content, "modified since you last viewed")
	})

	t.Run("succeeds when version matches", func(t *testing.T) {
		// Fetch fresh from DB to get the actual stored version
		freshDraft, err := repo.GetWorkflowDraft(context.Background(), draft.ID)
		require.NoError(t, err)

		// Use the database version (which is what the code compares against)
		currentVersion := freshDraft.Version

		params := WriteWorkflowParams{
			ID:              draft.ID,
			Content:         validWorkflowYAML,
			ExpectedVersion: &currentVersion,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-no-conflict",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.False(t, resp.IsError, "Should succeed: %s", resp.Content)
	})
}

func TestWriteWorkflow_ValidationErrors(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")

	// Helper to create a draft with unique name
	createDraft := func(t *testing.T) *db.WorkflowDraft {
		now := time.Now()
		uniqueID := uuid.New().String()[:8]
		draft := &db.WorkflowDraft{
			ID:        uuid.New().String(),
			UserID:    "test-user",
			Name:      "placeholder-" + uniqueID,
			Slug:      "placeholder-" + uniqueID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		err := repo.CreateWorkflowDraft(context.Background(), draft)
		require.NoError(t, err)
		return draft
	}

	t.Run("returns error for empty content", func(t *testing.T) {
		draft := createDraft(t)
		params := WriteWorkflowParams{
			ID:      draft.ID,
			Content: "",
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-empty",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "content is required")
	})

	t.Run("returns error for missing name", func(t *testing.T) {
		draft := createDraft(t)
		noNameYAML := `entry: [agent]
nodes:
  - id: agent
    type: call_llm
`
		params := WriteWorkflowParams{
			ID:      draft.ID,
			Content: noNameYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-no-name",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "name is required")
	})

	t.Run("saves invalid YAML with validation errors", func(t *testing.T) {
		draft := createDraft(t)
		// YAML is valid but workflow structure is invalid (missing entry)
		invalidWorkflow := `name: invalid-workflow
nodes:
  - id: agent
    type: call_llm
`
		params := WriteWorkflowParams{
			ID:      draft.ID,
			Content: invalidWorkflow,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-invalid",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		// Should still save but report validation errors
		assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content) // Not an error - saved with warnings
		assert.Contains(t, resp.Content, "validation errors")

		// Verify it was still saved
		var result WriteWorkflowResult
		err = json.Unmarshal([]byte(resp.Metadata), &result)
		require.NoError(t, err)
		assert.NotEmpty(t, result.ID)
	})
}

func TestWriteWorkflow_ResponseStructure(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewWriteWorkflowTool(repo)
	ctx := createTestContext(t, "")

	// First create a draft
	// Note: slug must match what generateSlugFromName("test-workflow") produces
	now := time.Now()
	draft := &db.WorkflowDraft{
		ID:        uuid.New().String(),
		UserID:    "test-user",
		Name:      "test-workflow",
		Slug:      "test-workflow",
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := repo.CreateWorkflowDraft(context.Background(), draft)
	require.NoError(t, err)

	t.Run("returns structured response with all fields", func(t *testing.T) {
		params := WriteWorkflowParams{
			ID:      draft.ID,
			Content: validWorkflowYAML,
		}

		inputJSON, _ := json.Marshal(params)
		resp, err := tool.Run(ctx, ToolCall{
			ID:    "test-response",
			Name:  "write_workflow",
			Input: string(inputJSON),
		})

		require.NoError(t, err)
		assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)

		// Verify metadata is valid JSON
		var result WriteWorkflowResult
		err = json.Unmarshal([]byte(resp.Metadata), &result)
		require.NoError(t, err, "Metadata should be valid JSON")

		// Verify all fields are populated
		assert.NotEmpty(t, result.ID, "ID should be set")
		assert.NotEmpty(t, result.Name, "Name should be set")
		assert.NotEmpty(t, result.Slug, "Slug should be set")
		// Created should be false since we're updating an existing draft
		assert.False(t, result.Created, "Created should be false for update")

		// Verify slug matches the draft's slug (unchanged on update)
		assert.Equal(t, draft.Slug, result.Slug)

		// Verify response text contains key info
		assert.Contains(t, resp.Content, result.ID)
		assert.Contains(t, resp.Content, result.Slug)
	})
}

// =============================================================================
// CreateWorkflow Tests
// =============================================================================

func TestCreateWorkflow_DefaultTemplate(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewCreateWorkflowTool(repo)
	ctx := createTestContext(t, "")

	// Create with no args — should use default template and random name
	resp, err := tool.Run(ctx, ToolCall{ID: "test-1", Name: "create_workflow", Input: "{}"})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
	assert.Contains(t, resp.Content, "created successfully")

	// Parse metadata
	var result CreateWorkflowResult
	err = json.Unmarshal([]byte(resp.Metadata), &result)
	require.NoError(t, err, "Metadata should be valid JSON")
	assert.NotEmpty(t, result.ID)
	assert.NotEmpty(t, result.Name)
	assert.NotEmpty(t, result.Slug)

	// Verify draft exists in DB
	draft, err := repo.GetWorkflowDraft(context.Background(), result.ID)
	require.NoError(t, err)
	require.NotNil(t, draft)
	assert.Equal(t, result.Name, draft.Name)
	assert.NotEmpty(t, draft.Definition)
}

func TestCreateWorkflow_WithName(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewCreateWorkflowTool(repo)
	ctx := createTestContext(t, "")

	name := "my-custom-workflow"
	inputJSON, _ := json.Marshal(CreateWorkflowParams{Name: &name})
	resp, err := tool.Run(ctx, ToolCall{ID: "test-1", Name: "create_workflow", Input: string(inputJSON)})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)

	var result CreateWorkflowResult
	err = json.Unmarshal([]byte(resp.Metadata), &result)
	require.NoError(t, err)
	assert.Equal(t, name, result.Name)
	assert.Equal(t, "my-custom-workflow", result.Slug)

	// Verify the template has the name replaced
	draft, err := repo.GetWorkflowDraft(context.Background(), result.ID)
	require.NoError(t, err)
	assert.Contains(t, draft.Definition, "name: my-custom-workflow")
	assert.NotContains(t, draft.Definition, "name: agent")
}

func TestCreateWorkflow_WithContent(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewCreateWorkflowTool(repo)
	ctx := createTestContext(t, "")

	content := validWorkflowYAML
	inputJSON, _ := json.Marshal(CreateWorkflowParams{Content: &content})
	resp, err := tool.Run(ctx, ToolCall{ID: "test-1", Name: "create_workflow", Input: string(inputJSON)})
	require.NoError(t, err)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)

	var result CreateWorkflowResult
	err = json.Unmarshal([]byte(resp.Metadata), &result)
	require.NoError(t, err)
	// Name should be extracted from the YAML
	assert.Equal(t, "test-workflow", result.Name)
	assert.Equal(t, "test-workflow", result.Slug)

	// Verify content is stored as-is
	draft, err := repo.GetWorkflowDraft(context.Background(), result.ID)
	require.NoError(t, err)
	assert.Equal(t, validWorkflowYAML, draft.Definition)
}

func TestCreateWorkflow_WithInvalidContent(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()

	tool := NewCreateWorkflowTool(repo)
	ctx := createTestContext(t, "")

	invalidYAML := "name: broken\nnodes: not-a-list"
	inputJSON, _ := json.Marshal(CreateWorkflowParams{Content: &invalidYAML})
	resp, err := tool.Run(ctx, ToolCall{ID: "test-1", Name: "create_workflow", Input: string(inputJSON)})
	require.NoError(t, err)
	// Should still succeed (saves with validation errors)
	assert.False(t, resp.IsError, "Should not be an error: %s", resp.Content)
	assert.Contains(t, resp.Content, "validation errors")

	var result CreateWorkflowResult
	err = json.Unmarshal([]byte(resp.Metadata), &result)
	require.NoError(t, err)
	assert.NotEmpty(t, result.ID)

	// Draft should exist but be marked invalid
	draft, err := repo.GetWorkflowDraft(context.Background(), result.ID)
	require.NoError(t, err)
	assert.False(t, draft.IsValid)
}