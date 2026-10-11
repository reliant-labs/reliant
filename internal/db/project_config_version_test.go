package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// TestProjectConfigVersion: the version token is what a cached reader trusts
// instead of re-reading the record, so it must change on EVERY rewrite —
// including one that writes identical content, and one from a writer whose
// clock stamped the same updated_at — and must stay put when nothing wrote.
func TestProjectConfigVersion(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := repo.GetProjectConfigVersion(ctx, "test-project"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no record: err = %v; want sql.ErrNoRows", err)
	}

	skills := `[{"name":"a"}]`
	upsert := func() {
		t.Helper()
		if err := repo.UpsertProjectConfigRecord(ctx, &ProjectConfigRecord{
			ProjectID: "test-project", DaemonID: "daemon-1", ProjectSkillsJSON: &skills,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	version := func() string {
		t.Helper()
		v, err := repo.GetProjectConfigVersion(ctx, "test-project")
		if err != nil || v == "" {
			t.Fatalf("GetProjectConfigVersion = %q, %v", v, err)
		}
		return v
	}

	upsert()
	v1 := version()
	if again := version(); again != v1 {
		t.Fatalf("version moved without a write: %q -> %q", v1, again)
	}

	upsert() // identical content
	v2 := version()
	if v2 == v1 {
		t.Fatalf("an identical rewrite kept version %q", v1)
	}

	// A write that leaves updated_at exactly where it was (another replica's
	// clock, or a writer that does not touch it) still moves the version.
	if _, err := repo.DB.ExecContext(ctx, `UPDATE project_configs SET project_skills_json = '[]', updated_at = updated_at WHERE project_id = $1`, "test-project"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if v3 := version(); v3 == v2 {
		t.Fatalf("a write with an unchanged updated_at kept version %q", v2)
	}
}
