package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestProjectConfigColumnReads: the single-column reads return exactly the
// column the full record would, and report a project with no record the same
// way (sql.ErrNoRows, unwrapped), so callers that switched from
// GetProjectConfigRecord keep their "nothing synced yet" handling.
func TestProjectConfigColumnReads(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	workflows := `[{"relative_path":".reliant/workflows/a.yaml","yaml_content":"name: a"}]`
	presets := `[{"name":"p","yaml_content":"name: p"}]`
	skills := `[{"name":"big"}]`
	if err := repo.UpsertProjectConfigRecord(ctx, &ProjectConfigRecord{
		ProjectID:            "test-project",
		DaemonID:             "daemon-1",
		ProjectWorkflowsJSON: &workflows,
		ProjectPresetsJSON:   &presets,
		ProjectSkillsJSON:    &skills,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	gotWorkflows, err := repo.GetProjectWorkflowsJSON(ctx, "test-project")
	if err != nil || gotWorkflows == nil || *gotWorkflows != workflows {
		t.Fatalf("GetProjectWorkflowsJSON = %v, %v; want %q", gotWorkflows, err, workflows)
	}
	gotPresets, err := repo.GetProjectPresetsJSON(ctx, "test-project")
	if err != nil || gotPresets == nil || *gotPresets != presets {
		t.Fatalf("GetProjectPresetsJSON = %v, %v; want %q", gotPresets, err, presets)
	}

	// A record whose column was never synced reads as nil, not "".
	if err := repo.UpsertProjectConfigRecord(ctx, &ProjectConfigRecord{
		ProjectID: "test-project", DaemonID: "daemon-1", ProjectSkillsJSON: &skills,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, err := repo.GetProjectPresetsJSON(ctx, "test-project"); err != nil || got != nil {
		t.Fatalf("unsynced presets = %v, %v; want nil, nil", got, err)
	}

	if _, err := repo.GetProjectWorkflowsJSON(ctx, "no-such-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no record: err = %v; want sql.ErrNoRows", err)
	}
	if _, err := repo.GetProjectPresetsJSON(ctx, "no-such-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no record: err = %v; want sql.ErrNoRows", err)
	}
}
