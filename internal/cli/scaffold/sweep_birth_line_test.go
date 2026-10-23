package scaffold

import (
	"path/filepath"
	"strings"
	"testing"
)

// The entity-birth progress line claimed "(+down)" — a down migration that is
// never written, because forge rolls forward only — and printed the
// migration's absolute path.
func TestBirthMigrationLineNamesOnlyWhatWasWritten(t *testing.T) {
	root := filepath.Join(t.TempDir(), "shop")
	got := birthMigrationLine(root, filepath.Join(root, "db", "migrations", "20260101000000_create_orders.up.sql"))
	if strings.Contains(got, "down") {
		t.Errorf("the line announces a down migration nothing writes: %q", got)
	}
	if want := "db/migrations/20260101000000_create_orders.up.sql"; !strings.HasSuffix(got, want) || strings.Contains(got, root) {
		t.Errorf("line = %q, want the project-relative %s", got, want)
	}
}
