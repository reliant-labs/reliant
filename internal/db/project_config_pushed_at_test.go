package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// TestProjectConfigPushedAtAndScenariosColumn: the narrow reads return exactly
// what the full record carries, and report a project with no record the way
// GetProjectConfigRecord does (sql.ErrNoRows, unwrapped), so the daemon sync's
// "nothing stored yet — apply" and the scenario RPCs' NotFound keep working.
func TestProjectConfigPushedAtAndScenariosColumn(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := repo.GetProjectConfigPushedAt(ctx, "test-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no record: pushed_at err = %v; want sql.ErrNoRows", err)
	}
	if _, err := repo.GetProjectScenariosJSON(ctx, "test-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no record: scenarios err = %v; want sql.ErrNoRows", err)
	}

	pushedAt := time.UnixMilli(1_700_000_123_456).UTC()
	scenarios := `[{"workflow_slug":"wf","name":"s1","yaml_content":"name: s1"}]`
	skills := `[{"name":"big"}]`
	if err := repo.UpsertProjectConfigRecord(ctx, &ProjectConfigRecord{
		ProjectID: "test-project", DaemonID: "daemon-1", PushedAt: pushedAt,
		ProjectScenariosJSON: &scenarios, ProjectSkillsJSON: &skills,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	gotPushedAt, err := repo.GetProjectConfigPushedAt(ctx, "test-project")
	if err != nil {
		t.Fatalf("GetProjectConfigPushedAt: %v", err)
	}
	record, err := repo.GetProjectConfigRecord(ctx, "test-project")
	if err != nil {
		t.Fatalf("GetProjectConfigRecord: %v", err)
	}
	if gotPushedAt.UnixMilli() != record.PushedAt.UnixMilli() || gotPushedAt.UnixMilli() != pushedAt.UnixMilli() {
		t.Fatalf("pushed_at = %v; record has %v; wrote %v", gotPushedAt, record.PushedAt, pushedAt)
	}

	gotScenarios, err := repo.GetProjectScenariosJSON(ctx, "test-project")
	if err != nil || gotScenarios == nil || *gotScenarios != scenarios {
		t.Fatalf("GetProjectScenariosJSON = %v, %v; want %q", gotScenarios, err, scenarios)
	}

	// A record whose scenarios were never synced reads as nil, not "".
	if err := repo.UpsertProjectConfigRecord(ctx, &ProjectConfigRecord{
		ProjectID: "test-project", DaemonID: "daemon-1", PushedAt: pushedAt, ProjectSkillsJSON: &skills,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, err := repo.GetProjectScenariosJSON(ctx, "test-project"); err != nil || got != nil {
		t.Fatalf("unsynced scenarios = %v, %v; want nil, nil", got, err)
	}
}
