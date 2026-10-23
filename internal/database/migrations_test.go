package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeMigrationName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "spaces", input: "add users table", want: "add_users_table"},
		{name: "mixed case", input: "Backfill Account Status", want: "backfill_account_status"},
		{name: "symbols", input: "add-users/table!", want: "add_users_table"},
		{name: "trim underscores", input: "__repair dirty state__", want: "repair_dirty_state"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMigrationName(tt.input)
			if got != tt.want {
				t.Fatalf("sanitizeMigrationName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCreateMigrationCreatesOnlyAnUpFile pins the roll-forward policy at the
// writer: `forge db migration new` scaffolds a forward migration and nothing
// that claims to reverse it.
func TestCreateMigrationCreatesOnlyAnUpFile(t *testing.T) {
	dir := t.TempDir()

	if err := CreateMigration(context.Background(), "Add Users Table", dir, nil); err != nil {
		t.Fatalf("CreateMigration() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected exactly one migration file (up only), got %v", names)
	}
	upFile := entries[0].Name()
	if !strings.HasSuffix(upFile, ".up.sql") {
		t.Fatalf("expected an .up.sql migration file, got %s", upFile)
	}
	if !strings.Contains(upFile, "add_users_table") {
		t.Fatalf("expected sanitized name in up migration, got %s", upFile)
	}

	upContents, err := os.ReadFile(filepath.Join(dir, upFile))
	if err != nil {
		t.Fatalf("ReadFile(up) error = %v", err)
	}
	if !strings.Contains(string(upContents), "Write your migration SQL below") {
		t.Fatalf("unexpected up migration contents: %s", string(upContents))
	}
}

func TestCreateMigrationRejectsEmptySanitizedName(t *testing.T) {
	dir := t.TempDir()

	err := CreateMigration(context.Background(), "!!!", dir, nil)
	if err == nil {
		t.Fatal("expected error for empty sanitized migration name")
	}
}
