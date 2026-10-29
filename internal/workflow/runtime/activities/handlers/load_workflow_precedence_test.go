// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A run its trigger started loads the project's workflow of a name before
// the owner's own account workflow of that name, exactly as its activation
// was admitted; an interactive chat keeps "yours wins for you".
func TestLoadWorkflowActivity_AutomationRunsTheProjectsWorkflow(t *testing.T) {
	t.Parallel()
	const userYAML = "name: release\ndescription: mine\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
	const projectYAML = "name: release\ndescription: the project's\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
	workflowsJSON := mustMarshalJSON(t, []map[string]string{
		{"slug": "release", "name": "release", "relative_path": "control-plane/.reliant/workflows/release.yaml", "yaml_content": projectYAML},
	})
	triggerID := "trigger-1"
	store := func(chat *db.Chat) *MockLoadWorkflowStore {
		return &MockLoadWorkflowStore{
			GetChatFunc:    func(context.Context, string) (*db.Chat, error) { return chat, nil },
			GetProjectFunc: func(context.Context, string) (*db.Project, error) { return &db.Project{ID: "p"}, nil },
			GetProjectWorkflowsJSONFunc: func(context.Context, string) (*string, error) {
				return &workflowsJSON, nil
			},
			GetUsableWorkflowBySlugFunc: func(_ context.Context, _, slug string) (*db.WorkflowDraft, error) {
				if slug == "release" {
					return &db.WorkflowDraft{Definition: userYAML}, nil
				}
				return nil, nil
			},
			GetWorkflowDraftBySlugFunc: func(context.Context, string, string) (*db.WorkflowDraft, error) { return nil, nil },
		}
	}
	load := func(chat *db.Chat) string {
		out, err := NewLoadWorkflowActivity(store(chat)).Execute(context.Background(), LoadWorkflowInput{ChatID: chat.ID, WorkflowName: "release"})
		require.NoError(t, err)
		return string(out.YAML)
	}

	assert.Contains(t, load(&db.Chat{ID: "auto", UserID: "u", ProjectID: "p", TriggerID: &triggerID}), "the project's")
	assert.Contains(t, load(&db.Chat{ID: "chat", UserID: "u", ProjectID: "p"}), "mine")
}
