// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	cfg "github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// setupTestPresetService creates a database and preset service for testing.
// Returns the service, repo, userID, and projectID.
func setupTestPresetService(t *testing.T) (*PresetService, *db.Repo, string, string) {
	t.Helper()

	repo := db.NewTestRepo(t)

	userID := uuid.New().String()
	projectID := uuid.New().String()
	now := time.Now().UTC()

	err := repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Test Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	})
	if err != nil {
		t.Fatalf("failed to create test project: %v", err)
	}

	service := NewPresetService(repo)
	return service, repo, userID, projectID
}

// insertStoredPresets inserts project presets into the project_configs table.
func insertStoredPresets(t *testing.T, repo *db.Repo, projectID string, presets []cfg.StoredPreset) {
	t.Helper()
	presetsJSON, err := json.Marshal(presets)
	if err != nil {
		t.Fatalf("failed to marshal presets: %v", err)
	}
	presetsStr := string(presetsJSON)
	now := time.Now().UTC()

	err = repo.UpsertProjectConfigRecord(context.Background(), &db.ProjectConfigRecord{
		ID:                 uuid.New().String(),
		ProjectID:          projectID,
		DaemonID:           "test-daemon",
		ProjectPresetsJSON: &presetsStr,
		PushedAt:           now,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if err != nil {
		t.Fatalf("failed to insert project config: %v", err)
	}
}

// createTestContext creates a context with the user ID for auth
func createTestContext(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

func TestPresetService_ListPresets_LayeredLoading(t *testing.T) {
	service, repo, userID, projectID := setupTestPresetService(t)

	// Store a project preset in the DB config
	projectPresetYAML := `name: project-preset
description: A project preset
tag: agent
params:
  model:
    id: claude-4.5-sonnet
  temperature: 0.5
  system_prompt: test
`
	insertStoredPresets(t, repo, projectID, []cfg.StoredPreset{
		{Name: "project-preset", YAMLContent: projectPresetYAML, ContentHash: "test"},
	})

	ctx := createTestContext(userID)

	t.Run("ListPresets_ReturnsProjectPresets", func(t *testing.T) {
		req := connect.NewRequest(&reliantv1.ListPresetsRequest{
			ProjectId: projectID,
		})

		resp, err := service.ListPresets(ctx, req)
		if err != nil {
			t.Fatalf("ListPresets failed: %v", err)
		}

		// Should include the project preset
		found := false
		for _, p := range resp.Msg.Presets {
			if p.Name == "project-preset" {
				found = true
				if p.Source != "project" {
					t.Errorf("expected source 'project', got %q", p.Source)
				}
				modelStruct := p.Params["model"].GetStructValue()
				if modelStruct == nil || modelStruct.Fields["id"].GetStringValue() != "claude-4.5-sonnet" {
					t.Errorf("expected model.id 'claude-4.5-sonnet', got %v", p.Params["model"])
				}
				break
			}
		}
		if !found {
			for _, p := range resp.Msg.Presets {
				t.Logf("preset: name=%q slug=%q source=%q tag=%q params=%v", p.Name, p.Slug, p.Source, p.Tag, p.Params)
			}
			t.Fatalf("project preset not found in response")
		}
	})

	t.Run("ListPresets_UserOverridesProject", func(t *testing.T) {
		// Create a user preset with the same slug as the project preset
		userPreset := &db.Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "project-preset",
			Slug:   "project-preset",
			Tag:    "agent",
			Params: map[string]interface{}{
				"model":       map[string]interface{}{"id": "claude-4.6-opus"},
				"temperature": 0.9,
			},
		}
		_, err := repo.CreatePreset(ctx, userPreset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		req := connect.NewRequest(&reliantv1.ListPresetsRequest{
			ProjectId: projectID,
		})

		resp, err := service.ListPresets(ctx, req)
		if err != nil {
			t.Fatalf("ListPresets failed: %v", err)
		}

		// Should return the user preset (overrides project preset)
		found := false
		for _, p := range resp.Msg.Presets {
			if p.Slug == "project-preset" {
				found = true
				if p.Source != "user" {
					t.Errorf("expected source 'user' (override), got %q", p.Source)
				}
				modelStruct := p.Params["model"].GetStructValue()
				if modelStruct == nil || modelStruct.Fields["id"].GetStringValue() != "claude-4.6-opus" {
					t.Errorf("expected model.id 'claude-4.6-opus' (user override), got %v", p.Params["model"])
				}
				break
			}
		}
		if !found {
			t.Error("preset not found in response")
		}
	})
}

func TestPresetService_GetPreset_UserOverridesProject(t *testing.T) {
	service, repo, userID, projectID := setupTestPresetService(t)

	// Store a project preset in the DB config
	projectPresetYAML := `name: override-test
description: Project version
tag: agent
params:
  model:
    id: claude-4.5-sonnet
  system_prompt: test
`
	insertStoredPresets(t, repo, projectID, []cfg.StoredPreset{
		{Name: "override-test", YAMLContent: projectPresetYAML, ContentHash: "test"},
	})

	ctx := createTestContext(userID)

	t.Run("GetPreset_ReturnsProjectPresetWhenNoUserOverride", func(t *testing.T) {
		req := connect.NewRequest(&reliantv1.GetPresetRequest{
			ProjectId: projectID,
			Name:      "override-test",
		})

		resp, err := service.GetPreset(ctx, req)
		if err != nil {
			t.Fatalf("GetPreset failed: %v", err)
		}

		if resp.Msg.Preset.Source != "project" {
			t.Errorf("expected source 'project', got %q", resp.Msg.Preset.Source)
		}
		modelStruct := resp.Msg.Preset.Params["model"].GetStructValue()
		if modelStruct == nil || modelStruct.Fields["id"].GetStringValue() != "claude-4.5-sonnet" {
			t.Errorf("expected model.id 'claude-4.5-sonnet', got %v", resp.Msg.Preset.Params["model"])
		}
	})

	t.Run("GetPreset_ReturnsUserPresetWhenOverrideExists", func(t *testing.T) {
		// Create a user preset with the same slug
		userPreset := &db.Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "override-test",
			Slug:   "override-test",
			Tag:    "agent",
			Params: map[string]interface{}{
				"model": map[string]interface{}{"id": "claude-4.6-opus"},
			},
		}
		_, err := repo.CreatePreset(ctx, userPreset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		req := connect.NewRequest(&reliantv1.GetPresetRequest{
			ProjectId: projectID,
			Name:      "override-test",
		})

		resp, err := service.GetPreset(ctx, req)
		if err != nil {
			t.Fatalf("GetPreset failed: %v", err)
		}

		// Should return user preset
		if resp.Msg.Preset.Source != "user" {
			t.Errorf("expected source 'user', got %q", resp.Msg.Preset.Source)
		}
		modelStruct := resp.Msg.Preset.Params["model"].GetStructValue()
		if modelStruct == nil || modelStruct.Fields["id"].GetStringValue() != "claude-4.6-opus" {
			t.Errorf("expected model.id 'claude-4.6-opus', got %v", resp.Msg.Preset.Params["model"])
		}
	})
}

func TestPresetService_ListPresetsForWorkflow_UsesUserWorkflowDrafts(t *testing.T) {
	service, repo, userID, projectID := setupTestPresetService(t)

	ctx := createTestContext(userID)

	workflowYAML := `
name: Custom Agent Draft
apiVersion: "0.0.5"
presets:
  tag: agent
entry: [ask]
inputs:
  model:
    type: model
  system_prompt:
    type: string
    default: ""
  thinking_level:
    type: enum
    enum: [low, medium, high, xhigh]
    default: high
  permission:
    type: string
    default: "orchestrator"
  tools:
    type: tools
    default: ["tag:default"]
  skills:
    type: array
    default: []
  spawn_presets:
    type: preset
    tags: [agent]
    multi: true
    default: [general, researcher, code_reviewer]
nodes:
  - id: ask
    type: call_llm
    args:
      model: "{{inputs.model}}"
      messages:
        - role: user
          content: test
`
	err := repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
		ID:         uuid.NewString(),
		UserID:     userID,
		Name:       "Custom Agent Draft",
		Slug:       "custom-agent-draft",
		Definition: workflowYAML,
		IsValid:    true,
		IsHidden:   false,
		Version:    1,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowDraft failed: %v", err)
	}

	resp, err := service.ListPresetsForWorkflow(ctx, connect.NewRequest(&reliantv1.ListPresetsForWorkflowRequest{
		ProjectId:    projectID,
		WorkflowName: "Custom Agent Draft",
	}))
	if err != nil {
		t.Fatalf("ListPresetsForWorkflow failed: %v", err)
	}

	presetNames := make(map[string]bool)
	for _, p := range resp.Msg.Presets {
		presetNames[p.Name] = true
	}

	t.Run("returns builtin agent presets for compatible user drafts", func(t *testing.T) {
		if !presetNames["general"] {
			t.Fatalf("expected builtin agent preset 'general' for user draft, got %v", presetNames)
		}
		if !presetNames["researcher"] {
			t.Fatalf("expected builtin agent preset 'researcher' for user draft, got %v", presetNames)
		}
	})
}

func TestPresetService_CreatePreset(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)

	ctx := createTestContext(userID)

	t.Run("CreatePreset_Success", func(t *testing.T) {
		req := connect.NewRequest(&reliantv1.CreatePresetRequest{
			ProjectId:   projectID,
			Name:        "My Custom Preset",
			Description: "A custom preset for testing",
			Tag:         "agent",
		})

		resp, err := service.CreatePreset(ctx, req)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		if !resp.Msg.Success {
			t.Errorf("expected success, got error: %s", resp.Msg.Error)
		}

		if resp.Msg.Preset == nil {
			t.Fatal("expected preset in response")
		}

		if resp.Msg.Preset.Name != "My Custom Preset" {
			t.Errorf("Name = %q, want 'My Custom Preset'", resp.Msg.Preset.Name)
		}

		if resp.Msg.Preset.Source != "user" {
			t.Errorf("Source = %q, want 'user'", resp.Msg.Preset.Source)
		}

		// Slug should be generated from name
		if resp.Msg.Preset.Slug != "my-custom-preset" {
			t.Errorf("Slug = %q, want 'my-custom-preset'", resp.Msg.Preset.Slug)
		}
	})

	t.Run("CreatePreset_PreservesToolParams", func(t *testing.T) {
		toolParams, err := structpb.NewValue([]interface{}{"tag:default"})
		if err != nil {
			t.Fatalf("failed to build tools value: %v", err)
		}

		req := connect.NewRequest(&reliantv1.CreatePresetRequest{
			ProjectId: projectID,
			Name:      "Tool Preset",
			Tag:       "agent",
			Params: map[string]*structpb.Value{
				"tools": toolParams,
			},
		})

		resp, err := service.CreatePreset(ctx, req)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}
		if !resp.Msg.Success {
			t.Fatalf("expected success, got error: %s", resp.Msg.Error)
		}

		tools := resp.Msg.Preset.Params["tools"].GetListValue()
		if tools == nil || len(tools.Values) != 1 {
			t.Fatalf("expected tools list of len 1, got %v", resp.Msg.Preset.Params["tools"])
		}
		if tools.Values[0].GetStringValue() != "tag:default" {
			t.Errorf("unexpected tools list: %v", tools.Values)
		}
	})

	t.Run("CreatePreset_DuplicateSlug", func(t *testing.T) {
		// First create
		req := connect.NewRequest(&reliantv1.CreatePresetRequest{
			ProjectId: projectID,
			Name:      "Duplicate Test",
			Tag:       "agent",
		})

		resp, err := service.CreatePreset(ctx, req)
		if err != nil {
			t.Fatalf("First CreatePreset failed: %v", err)
		}
		if !resp.Msg.Success {
			t.Fatalf("First CreatePreset should succeed")
		}

		// Second create with same name should fail
		resp, err = service.CreatePreset(ctx, req)
		if err != nil {
			t.Fatalf("Second CreatePreset returned error: %v", err)
		}

		if resp.Msg.Success {
			t.Error("expected failure for duplicate slug")
		}
		if resp.Msg.Error == "" {
			t.Error("expected error message for duplicate slug")
		}
	})

	t.Run("CreatePreset_InvalidName", func(t *testing.T) {
		req := connect.NewRequest(&reliantv1.CreatePresetRequest{
			ProjectId: projectID,
			Name:      "", // Empty name
			Tag:       "agent",
		})

		_, err := service.CreatePreset(ctx, req)
		if err == nil {
			t.Error("expected error for empty name")
		}
	})
}

func TestPresetService_UpdatePreset(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)

	ctx := createTestContext(userID)

	// Create a preset first
	createReq := connect.NewRequest(&reliantv1.CreatePresetRequest{
		ProjectId:   projectID,
		Name:        "Update Test Preset",
		Description: "Original description",
		Tag:         "agent",
	})

	createResp, err := service.CreatePreset(ctx, createReq)
	if err != nil || !createResp.Msg.Success {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	t.Run("UpdatePreset_Success", func(t *testing.T) {
		updateReq := connect.NewRequest(&reliantv1.UpdatePresetRequest{
			ProjectId:      projectID,
			Name:           "update-test-preset", // Use slug, not display name
			NewName:        ptr.Of("Updated Preset Name"),
			NewDescription: ptr.Of("Updated description"),
		})

		resp, err := service.UpdatePreset(ctx, updateReq)
		if err != nil {
			t.Fatalf("UpdatePreset failed: %v", err)
		}

		if !resp.Msg.Success {
			t.Errorf("expected success, got error: %s", resp.Msg.Error)
		}

		if resp.Msg.Preset.Name != "Updated Preset Name" {
			t.Errorf("Name = %q, want 'Updated Preset Name'", resp.Msg.Preset.Name)
		}

		if resp.Msg.Preset.Description != "Updated description" {
			t.Errorf("Description = %q, want 'Updated description'", resp.Msg.Preset.Description)
		}
	})

	t.Run("UpdatePreset_NotFound", func(t *testing.T) {
		updateReq := connect.NewRequest(&reliantv1.UpdatePresetRequest{
			ProjectId:      projectID,
			Name:           "nonexistent-preset",
			NewDescription: ptr.Of("New description"),
		})

		resp, err := service.UpdatePreset(ctx, updateReq)
		if err != nil {
			t.Fatalf("UpdatePreset returned error: %v", err)
		}

		if resp.Msg.Success {
			t.Error("expected failure for nonexistent preset")
		}
	})
}

func TestPresetService_DeletePreset(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)

	ctx := createTestContext(userID)

	// Create a preset first
	createReq := connect.NewRequest(&reliantv1.CreatePresetRequest{
		ProjectId: projectID,
		Name:      "Delete Test Preset",
		Tag:       "agent",
	})

	createResp, err := service.CreatePreset(ctx, createReq)
	if err != nil || !createResp.Msg.Success {
		t.Fatalf("CreatePreset failed: %v", err)
	}

	t.Run("DeletePreset_Success", func(t *testing.T) {
		deleteReq := connect.NewRequest(&reliantv1.DeletePresetRequest{
			ProjectId: projectID,
			Name:      "delete-test-preset", // Use slug, not display name
		})

		resp, err := service.DeletePreset(ctx, deleteReq)
		if err != nil {
			t.Fatalf("DeletePreset failed: %v", err)
		}

		if !resp.Msg.Success {
			t.Errorf("expected success, got error: %s", resp.Msg.Error)
		}

		// Verify it's gone
		getReq := connect.NewRequest(&reliantv1.GetPresetRequest{
			ProjectId: projectID,
			Name:      "delete-test-preset", // Use slug, not display name
		})

		_, err = service.GetPreset(ctx, getReq)
		if err == nil {
			t.Error("expected error when getting deleted preset")
		}
	})

	t.Run("DeletePreset_NotFound", func(t *testing.T) {
		deleteReq := connect.NewRequest(&reliantv1.DeletePresetRequest{
			ProjectId: projectID,
			Name:      "nonexistent-preset",
		})

		resp, err := service.DeletePreset(ctx, deleteReq)
		if err != nil {
			t.Fatalf("DeletePreset returned error: %v", err)
		}

		if resp.Msg.Success {
			t.Error("expected failure for nonexistent preset")
		}
	})
}

func TestPresetService_ListPresets_IncludeHidden(t *testing.T) {
	service, _, userID, projectID := setupTestPresetService(t)

	ctx := createTestContext(userID)

	t.Run("ListPresets_WithoutIncludeHidden_ExcludesHiddenPresets", func(t *testing.T) {
		req := connect.NewRequest(&reliantv1.ListPresetsRequest{
			ProjectId:     projectID,
			IncludeHidden: false,
		})

		resp, err := service.ListPresets(ctx, req)
		if err != nil {
			t.Fatalf("ListPresets failed: %v", err)
		}

		t.Logf("ListPresets(includeHidden=false) returned %d presets", len(resp.Msg.Presets))
		for _, p := range resp.Msg.Presets {
			t.Logf("  name=%s slug=%s source=%s is_hidden=%v", p.Name, p.Slug, p.Source, p.IsHidden)
			if p.IsHidden {
				t.Errorf("Hidden preset %q should NOT be returned when includeHidden=false", p.Slug)
			}
		}
	})

	t.Run("ListPresets_WithIncludeHidden_IncludesAllPresets", func(t *testing.T) {
		reqHidden := connect.NewRequest(&reliantv1.ListPresetsRequest{
			ProjectId:     projectID,
			IncludeHidden: true,
		})

		respHidden, err := service.ListPresets(ctx, reqHidden)
		if err != nil {
			t.Fatalf("ListPresets failed: %v", err)
		}

		reqVisible := connect.NewRequest(&reliantv1.ListPresetsRequest{
			ProjectId:     projectID,
			IncludeHidden: false,
		})

		respVisible, err := service.ListPresets(ctx, reqVisible)
		if err != nil {
			t.Fatalf("ListPresets failed: %v", err)
		}

		t.Logf("ListPresets(includeHidden=true) returned %d presets", len(respHidden.Msg.Presets))
		t.Logf("ListPresets(includeHidden=false) returned %d presets", len(respVisible.Msg.Presets))

		// includeHidden=true should return at least as many presets as includeHidden=false
		if len(respHidden.Msg.Presets) < len(respVisible.Msg.Presets) {
			t.Errorf("includeHidden=true returned fewer presets (%d) than includeHidden=false (%d)",
				len(respHidden.Msg.Presets), len(respVisible.Msg.Presets))
		}
	})
}
