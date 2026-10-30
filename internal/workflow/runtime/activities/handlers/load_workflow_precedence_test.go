// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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
			// No pin: these loads read the live catalog (the pin is load_workflow_pin_test.go's).
			PinProjectCatalogFunc: func(context.Context, db.CatalogPinScope) (string, error) { return "", nil },
		}
	}
	load := func(chat *db.Chat) string {
		out, err := NewLoadWorkflowActivity(store(chat)).Execute(context.Background(), LoadWorkflowInput{ChatID: chat.ID, WorkflowName: "release"})
		require.NoError(t, err)
		return string(out.YAML)
	}

	assert.Contains(t, load(&db.Chat{ID: "auto", UserID: "u", ProjectID: "p", TriggerID: &triggerID, Origin: core.ChatOriginAutomation}), "the project's")
	// Deleting the trigger clears the chat's trigger id (ON DELETE SET NULL),
	// not what launched it: a running or paused automation keeps loading the
	// project's definitions.
	assert.Contains(t, load(&db.Chat{ID: "auto", UserID: "u", ProjectID: "p", Origin: core.ChatOriginAutomation}), "the project's",
		"an automation whose trigger was deleted stays project-first")
	assert.Contains(t, load(&db.Chat{ID: "chat", UserID: "u", ProjectID: "p"}), "mine")
	assert.Contains(t, load(&db.Chat{ID: "run", UserID: "u", ProjectID: "p", Origin: core.ChatOriginManualRun}), "mine", "a person's Run now keeps yours-first")
}

// Both rules at once, on two machines: an automation's run on cicd loads
// cicd's project workflow, not the union's newest (the laptop's), and not the
// owner's same-named account workflow.
func TestLoadWorkflowActivity_AutomationOnAMachineRunsThatMachinesProjectWorkflow(t *testing.T) {
	t.Parallel()
	wf := func(description string) string {
		return "name: release\ndescription: " + description + "\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
	}
	catalog := func(description string) *string {
		j := mustMarshalJSON(t, []map[string]string{{"slug": "release", "name": "release", "relative_path": "control-plane/.reliant/workflows/release.yaml", "yaml_content": wf(description)}})
		return &j
	}
	triggerID, cicd := "trigger-1", "cicd"
	chat := &db.Chat{ID: "auto", UserID: "u", ProjectID: "p", TriggerID: &triggerID, Origin: core.ChatOriginAutomation, ActiveDaemonID: &cicd}
	store := &MockLoadWorkflowStore{
		GetChatFunc:                 func(context.Context, string) (*db.Chat, error) { return chat, nil },
		GetProjectFunc:              func(context.Context, string) (*db.Project, error) { return &db.Project{ID: "p"}, nil },
		GetProjectWorkflowsJSONFunc: func(context.Context, string) (*string, error) { return catalog("the laptop's"), nil },
		GetProjectMachineWorkflowsJSONFunc: func(_ context.Context, _, daemonID string) (*string, bool, error) {
			require.Equal(t, cicd, daemonID)
			return catalog("cicd's"), true, nil
		},
		GetUsableWorkflowBySlugFunc: func(context.Context, string, string) (*db.WorkflowDraft, error) {
			return &db.WorkflowDraft{Definition: wf("mine")}, nil
		},
		GetWorkflowDraftBySlugFunc: func(context.Context, string, string) (*db.WorkflowDraft, error) { return nil, nil },
		// No pin: these loads read the live catalog (the pin is load_workflow_pin_test.go's).
		PinProjectCatalogFunc: func(context.Context, db.CatalogPinScope) (string, error) { return "", nil },
	}
	out, err := NewLoadWorkflowActivity(store).Execute(context.Background(), LoadWorkflowInput{ChatID: chat.ID, WorkflowName: "release"})
	require.NoError(t, err)
	assert.Contains(t, string(out.YAML), "cicd's")
}
