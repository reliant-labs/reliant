package database

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// newMigration runs CreateMigration into a fresh directory and returns the
// file it wrote.
func newMigration(t *testing.T, opts *MigrationOptions) string {
	t.Helper()
	dir := t.TempDir()
	if err := CreateMigration(context.Background(), "add_x", dir, opts); err != nil {
		t.Fatalf("CreateMigration: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("want one file, got %v (%v)", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// `forge db migration new --backward-compatible/--breaks-rollback <why>`
// writes a declaration the migrator and the linter both read, with its reason.
func TestCreateMigration_WritesTheCompatDeclaration(t *testing.T) {
	body := newMigration(t, &MigrationOptions{BackwardCompatible: "additive\ncolumn"})
	if c, b := compat.Declarations(body); len(c) != 1 || c[0].Reason != "additive column" || len(b) != 0 {
		t.Errorf("--backward-compatible wrote %+v / %+v:\n%s", c, b, body)
	}
	if !compat.Declared(body) {
		t.Error("the migrator does not read the written line as backward-compatible")
	}

	body = newMigration(t, &MigrationOptions{BreaksRollback: "drops plans.y"})
	if c, b := compat.Declarations(body); len(b) != 1 || b[0].Reason != "drops plans.y" || len(c) != 0 {
		t.Errorf("--breaks-rollback wrote %+v / %+v:\n%s", c, b, body)
	}
	if compat.Declared(body) {
		t.Error("a breaks-rollback migration read as backward-compatible")
	}

	if err := CreateMigration(context.Background(), "x", t.TempDir(),
		&MigrationOptions{BackwardCompatible: "a", BreaksRollback: "b"}); err == nil {
		t.Error("both declarations at once were accepted")
	}
}

// In a project that requires a declaration, a file created without one shows
// both choices as examples the author replaces — and the examples are not
// themselves a declaration.
func TestCreateMigration_RequiredDeclarationIsLeftForTheAuthor(t *testing.T) {
	body := newMigration(t, &MigrationOptions{RequireCompatDeclaration: true})
	if !strings.Contains(body, compat.Directive) || !strings.Contains(body, compat.BreaksRollbackDirective) {
		t.Errorf("the required-declaration hint does not name both choices:\n%s", body)
	}
	if c, b := compat.Declarations(body); len(c)+len(b) != 0 {
		t.Errorf("the hint was read as a declaration: %+v / %+v", c, b)
	}
	if plain := newMigration(t, nil); strings.Contains(plain, "forge:breaks-rollback") {
		t.Errorf("a project that does not require a declaration got the hint:\n%s", plain)
	}
}
