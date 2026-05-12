// Copyright (c) 2025 Reliant Labs
package handlers

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

// TestLoadBuiltinWorkflow tests that builtin:// prefix loads from embedded FS
func TestLoadBuiltinWorkflow(t *testing.T) {
	t.Run("loads builtin agent workflow", func(t *testing.T) {
		wf, err := loadBuiltinWorkflow("agent")
		require.NoError(t, err)
		assert.Equal(t, "agent", wf.Name)
	})

	t.Run("fails for non-existent builtin", func(t *testing.T) {
		_, err := loadBuiltinWorkflow("builtin://non-existent-workflow")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "builtin workflow not found")
	})
}

// TestLoadBuiltinWorkflowWithRaw tests raw YAML is returned alongside parsed workflow
func TestLoadBuiltinWorkflowWithRaw(t *testing.T) {
	t.Run("returns raw YAML and parsed workflow", func(t *testing.T) {
		yamlData, templated, err := loadBuiltinWorkflowWithRaw("agent")
		require.NoError(t, err)

		// Should have raw YAML data
		assert.NotEmpty(t, yamlData)
		assert.Contains(t, string(yamlData), "name:")

		// Should have parsed workflow
		assert.NotNil(t, templated)
		assert.NotNil(t, templated.Workflow)
		assert.Equal(t, "agent", templated.Workflow.Name)
	})
}

func TestLoadWorkflowActivity_LoadsProjectWorkflowFromStoredConfig(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	projectPath := t.TempDir()

	workflowYAML := `
name: get-it-right
apiVersion: v2
entry: [ask]
inputs:
  model:
    type: model
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`

	now := time.Now().UTC()
	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Project Workflows",
		Path:       projectPath,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	// Simulate daemon sync by storing workflow in project_config_records
	workflowsJSON := mustMarshalJSON(t, []map[string]string{
		{"slug": "get-it-right", "name": "get-it-right", "yaml_content": workflowYAML, "content_hash": "abc123"},
	})
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:            projectID,
		DaemonID:             "daemon-" + uuid.NewString(),
		ProjectWorkflowsJSON: &workflowsJSON,
		PushedAt:             now,
	}))

	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     userID,
		ProjectID:  projectID,
		Title:      "test",
		State:      db.ChatStateIdle,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	activity := NewLoadWorkflowActivity(repo)
	output, err := activity.Execute(ctx, LoadWorkflowInput{
		ChatID:       chatID,
		WorkflowName: "get-it-right",
	})
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.NotEmpty(t, output.YAML)
	assert.NotEmpty(t, output.WorkflowJSON)
}

func TestLoadWorkflowActivity_LoadsProjectWorkflowByName(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()

	ctx := context.Background()
	userID := "user-" + uuid.NewString()
	projectPath := t.TempDir()

	workflowYAML := `
name: blog-content-pipeline
apiVersion: v2
entry: [ask]
inputs:
  model:
    type: model
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
`

	now := time.Now().UTC()
	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Project Workflows",
		Path:       projectPath,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	// Daemon derives slug from the YAML name field, not the filename
	workflowsJSON := mustMarshalJSON(t, []map[string]string{
		{"slug": "blog-content-pipeline", "name": "blog-content-pipeline", "yaml_content": workflowYAML, "content_hash": "abc123"},
	})
	require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
		ProjectID:            projectID,
		DaemonID:             "daemon-" + uuid.NewString(),
		ProjectWorkflowsJSON: &workflowsJSON,
		PushedAt:             now,
	}))

	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		UserID:     userID,
		ProjectID:  projectID,
		Title:      "test",
		State:      db.ChatStateIdle,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	activity := NewLoadWorkflowActivity(repo)

	// Looking up by the YAML name should work even though slug differs
	output, err := activity.Execute(ctx, LoadWorkflowInput{
		ChatID:       chatID,
		WorkflowName: "blog-content-pipeline",
	})
	require.NoError(t, err)
	require.NotNil(t, output)
	assert.NotEmpty(t, output.YAML)
	assert.NotEmpty(t, output.WorkflowJSON)
}

func mustMarshalJSON(t *testing.T, v interface{}) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}
