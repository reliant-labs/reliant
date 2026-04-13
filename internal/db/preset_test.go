// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// setupPresetTestDB creates an in-memory database with migrations for preset testing
func setupPresetTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}
	return db
}

func TestPresetCRUD(t *testing.T) {
	db := setupPresetTestDB(t)
	defer db.Close()

	repo := NewRepo(db)
	ctx := context.Background()
	userID := uuid.New().String()

	t.Run("CreatePreset", func(t *testing.T) {
		preset := &Preset{
			ID:          uuid.New().String(),
			UserID:      userID,
			Name:        "Test Preset",
			Slug:        "test-preset",
			Description: ptr.Of("A test preset"),
			Tag:         "agent",
			Params: map[string]interface{}{
				"model":       map[string]interface{}{"id": "claude-sonnet"},
				"temperature": 0.7,
			},
		}

		saved, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		if saved.ID != preset.ID {
			t.Errorf("ID = %q, want %q", saved.ID, preset.ID)
		}
		if saved.Name != preset.Name {
			t.Errorf("Name = %q, want %q", saved.Name, preset.Name)
		}
		if saved.Tag != preset.Tag {
			t.Errorf("Tag = %q, want %q", saved.Tag, preset.Tag)
		}
		modelParam, ok := saved.Params["model"].(map[string]interface{})
		if !ok || modelParam["id"] != "claude-sonnet" {
			t.Errorf("Params[model] = %v, want {id: claude-sonnet}", saved.Params["model"])
		}
	})

	t.Run("CreatePreset_PreservesToolArrays", func(t *testing.T) {
		preset := &Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "Tool Preset",
			Slug:   "tool-preset",
			Tag:    "agent",
			Params: map[string]interface{}{
				"tools": []interface{}{"tag:default"},
			},
		}

		saved, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		tools, ok := saved.Params["tools"].([]interface{})
		if !ok {
			t.Fatalf("Params[tools] has unexpected type %T", saved.Params["tools"])
		}
		if len(tools) != 1 || tools[0] != "tag:default" {
			t.Errorf("Params[tools] = %v, want [tag:default]", tools)
		}
	})

	t.Run("GetPresetBySlug", func(t *testing.T) {
		// Create a preset first
		preset := &Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "Get Test",
			Slug:   "get-test",
			Tag:    "agent",
			Params: map[string]interface{}{"model": "test"},
		}
		_, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// Get it back
		found, err := repo.GetPresetBySlug(ctx, userID, "get-test")
		if err != nil {
			t.Fatalf("GetPresetBySlug failed: %v", err)
		}
		if found == nil {
			t.Fatal("expected preset, got nil")
		}
		if found.Name != "Get Test" {
			t.Errorf("Name = %q, want Get Test", found.Name)
		}
	})

	t.Run("GetPresetBySlug_NotFound", func(t *testing.T) {
		found, err := repo.GetPresetBySlug(ctx, userID, "nonexistent")
		// Should return sql.ErrNoRows when not found
		if err == nil {
			t.Error("expected error for nonexistent preset")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("expected sql.ErrNoRows, got %v", err)
		}
		if found != nil {
			t.Errorf("expected nil, got preset with name %q", found.Name)
		}
	})

	t.Run("ListUserPresets", func(t *testing.T) {
		// Create multiple presets
		for i := 0; i < 3; i++ {
			preset := &Preset{
				ID:     uuid.New().String(),
				UserID: userID,
				Name:   "List Test " + string(rune('A'+i)),
				Slug:   "list-test-" + string(rune('a'+i)),
				Tag:    "agent",
				Params: map[string]interface{}{},
			}
			_, err := repo.CreatePreset(ctx, preset)
			if err != nil {
				t.Fatalf("CreatePreset failed: %v", err)
			}
		}

		// List all user presets
		presets, err := repo.ListUserPresets(ctx, userID)
		if err != nil {
			t.Fatalf("ListUserPresets failed: %v", err)
		}

		// Should have at least the 3 we just created plus any from earlier tests
		if len(presets) < 3 {
			t.Errorf("expected at least 3 presets, got %d", len(presets))
		}
	})

	t.Run("ListPresetsByTag", func(t *testing.T) {
		// Create presets with different tags
		orchPreset := &Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "Orchestrator Preset",
			Slug:   "orch-preset",
			Tag:    "orchestrator",
			Params: map[string]interface{}{},
		}
		_, err := repo.CreatePreset(ctx, orchPreset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// List by tag "orchestrator"
		presets, err := repo.ListPresetsByTag(ctx, userID, "orchestrator", "")
		if err != nil {
			t.Fatalf("ListPresetsByTag failed: %v", err)
		}

		if len(presets) < 1 {
			t.Errorf("expected at least 1 orchestrator preset, got %d", len(presets))
		}

		// Verify all returned presets have the correct tag
		for _, p := range presets {
			if p.Tag != "orchestrator" {
				t.Errorf("expected tag 'orchestrator', got %q", p.Tag)
			}
		}
	})

	t.Run("UpdatePreset", func(t *testing.T) {
		// Create a preset
		preset := &Preset{
			ID:          uuid.New().String(),
			UserID:      userID,
			Name:        "Update Test",
			Slug:        "update-test",
			Description: ptr.Of("Original description"),
			Tag:         "agent",
			Params:      map[string]interface{}{"model": "original"},
		}
		saved, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// Update it
		saved.Name = "Updated Name"
		saved.Description = ptr.Of("Updated description")
		saved.Params = map[string]interface{}{"model": "updated"}

		updated, err := repo.UpdatePreset(ctx, saved)
		if err != nil {
			t.Fatalf("UpdatePreset failed: %v", err)
		}

		if updated.Name != "Updated Name" {
			t.Errorf("Name = %q, want Updated Name", updated.Name)
		}
		if *updated.Description != "Updated description" {
			t.Errorf("Description = %q, want Updated description", *updated.Description)
		}
		if updated.Params["model"] != "updated" {
			t.Errorf("Params[model] = %v, want updated", updated.Params["model"])
		}
	})

	t.Run("DeletePreset", func(t *testing.T) {
		// Create a preset
		preset := &Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "Delete Test",
			Slug:   "delete-test",
			Tag:    "agent",
			Params: map[string]interface{}{},
		}
		saved, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// Delete it
		err = repo.DeletePreset(ctx, saved.ID)
		if err != nil {
			t.Fatalf("DeletePreset failed: %v", err)
		}

		// Verify it's gone - should return sql.ErrNoRows
		found, err := repo.GetPreset(ctx, saved.ID)
		if err == nil {
			t.Error("expected error for deleted preset")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("expected sql.ErrNoRows, got %v", err)
		}
		if found != nil {
			t.Error("expected preset to be deleted, but it still exists")
		}
	})

	t.Run("UpsertPreset_Insert", func(t *testing.T) {
		preset := &Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "Upsert Insert Test",
			Slug:   "upsert-insert-test",
			Tag:    "agent",
			Params: map[string]interface{}{"model": "initial"},
		}

		result, err := repo.UpsertPreset(ctx, preset)
		if err != nil {
			t.Fatalf("UpsertPreset failed: %v", err)
		}

		if result.Name != "Upsert Insert Test" {
			t.Errorf("Name = %q, want Upsert Insert Test", result.Name)
		}
	})

	t.Run("UpsertPreset_ConflictUpdate", func(t *testing.T) {
		// Create initial preset
		preset := &Preset{
			ID:     uuid.New().String(),
			UserID: userID,
			Name:   "Upsert Conflict Test",
			Slug:   "upsert-conflict-test",
			Tag:    "agent",
			Params: map[string]interface{}{"model": "initial"},
		}
		_, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// Upsert with NEW ID but same slug - should trigger ON CONFLICT and update
		// This tests the actual upsert behavior: same (user_id, project_id, slug) but different ID
		updatedPreset := &Preset{
			ID:     uuid.New().String(), // NEW ID
			UserID: userID,              // Same user
			Name:   "Upsert Updated",
			Slug:   "upsert-conflict-test", // Same slug - triggers conflict
			Tag:    "agent",
			Params: map[string]interface{}{"model": "updated"},
		}

		result, err := repo.UpsertPreset(ctx, updatedPreset)
		if err != nil {
			t.Fatalf("UpsertPreset failed: %v", err)
		}

		if result.Name != "Upsert Updated" {
			t.Errorf("Name = %q, want Upsert Updated", result.Name)
		}
		if result.Params["model"] != "updated" {
			t.Errorf("Params[model] = %v, want updated", result.Params["model"])
		}
	})
}

func TestPresetProjectScope(t *testing.T) {
	db := setupPresetTestDB(t)
	defer db.Close()

	repo := NewRepo(db)
	ctx := context.Background()
	userID := uuid.New().String()
	projectID := uuid.New().String()

	// Create a project for project-scoped tests (no user FK, just project_id FK on presets)
	_, err := db.Exec(`INSERT INTO projects (id, user_id, name, path, created_at, updated_at) VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))`,
		projectID, userID, "Test Project", "/tmp/test-project")
	if err != nil {
		t.Fatalf("failed to create test project: %v", err)
	}

	t.Run("CreatePreset_WithProjectScope", func(t *testing.T) {
		preset := &Preset{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: &projectID,
			Name:      "Project Scoped Preset",
			Slug:      "project-scoped",
			Tag:       "agent",
			Params:    map[string]interface{}{"model": "claude-sonnet"},
		}

		saved, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		if saved.ProjectID == nil {
			t.Fatal("expected ProjectID to be set")
		}
		if *saved.ProjectID != projectID {
			t.Errorf("ProjectID = %q, want %q", *saved.ProjectID, projectID)
		}
	})

	t.Run("ListUserPresetsGlobal", func(t *testing.T) {
		// Create a global preset (no project)
		globalPreset := &Preset{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: nil, // Global
			Name:      "Global Preset",
			Slug:      "global-preset",
			Tag:       "agent",
			Params:    map[string]interface{}{},
		}
		_, err := repo.CreatePreset(ctx, globalPreset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// List global presets only
		presets, err := repo.ListUserPresetsGlobal(ctx, userID)
		if err != nil {
			t.Fatalf("ListUserPresetsGlobal failed: %v", err)
		}

		// All returned presets should have nil ProjectID
		for _, p := range presets {
			if p.ProjectID != nil {
				t.Errorf("expected nil ProjectID, got %q", *p.ProjectID)
			}
		}
	})

	t.Run("ListUserPresetsByProject", func(t *testing.T) {
		// List presets for specific project
		presets, err := repo.ListUserPresetsByProject(ctx, userID, projectID)
		if err != nil {
			t.Fatalf("ListUserPresetsByProject failed: %v", err)
		}

		// Should include both global and project-specific presets
		hasGlobal := false
		hasProjectSpecific := false
		for _, p := range presets {
			if p.ProjectID == nil {
				hasGlobal = true
			} else if *p.ProjectID == projectID {
				hasProjectSpecific = true
			}
		}

		if !hasGlobal {
			t.Error("expected global presets to be included")
		}
		if !hasProjectSpecific {
			t.Error("expected project-specific presets to be included")
		}
	})

	t.Run("GetPresetBySlugAndProject", func(t *testing.T) {
		// Create a project-scoped preset
		preset := &Preset{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: &projectID,
			Name:      "Project Lookup Test",
			Slug:      "project-lookup-test",
			Tag:       "agent",
			Params:    map[string]interface{}{},
		}
		_, err := repo.CreatePreset(ctx, preset)
		if err != nil {
			t.Fatalf("CreatePreset failed: %v", err)
		}

		// Find it with project scope
		found, err := repo.GetPresetBySlugAndProject(ctx, userID, "project-lookup-test", projectID)
		if err != nil {
			t.Fatalf("GetPresetBySlugAndProject failed: %v", err)
		}
		if found == nil {
			t.Fatal("expected preset, got nil")
		}
		if found.Name != "Project Lookup Test" {
			t.Errorf("Name = %q, want Project Lookup Test", found.Name)
		}
	})
}
