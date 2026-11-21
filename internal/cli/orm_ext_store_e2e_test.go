//go:build e2e

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// A real generate must collect package-level query signatures over the ORM
// it just wrote, without publishing a locally shadowed predeclared type.
func TestE2EORMExtStoreGeneratesAndBuilds(t *testing.T) {
	t.Parallel()
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "extapp", "--mod", "example.com/extapp", "--service", "widget")
	project := filepath.Join(dir, "extapp")
	addCorpusForgePkgReplace(t, project)
	writeCollideFile(t, filepath.Join(project, "db", "migrations", "20260101000000_create_notes.up.sql"),
		"CREATE TABLE notes (id TEXT PRIMARY KEY);\n")
	db := filepath.Join(project, "internal", "db")
	writeCollideFile(t, filepath.Join(db, "note_repo_ext.go"), `package db

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
)

func FindNote(ctx context.Context, db orm.Context, id string, _ rune) (*Note, error) {
	return GetNoteByID(ctx, db, id)
}

func PrivateNote(context.Context, orm.Context, complex64) {}
`)
	writeCollideFile(t, filepath.Join(db, "shadow.go"), "package db\n\ntype complex64 struct{}\n")
	writeCollideFile(t, filepath.Join(db, "ext_consumer.go"), `package db

import "context"

var _ func(NoteExtStore, context.Context, string, rune) (*Note, error) = NoteExtStore.FindNote
`)

	runCmd(t, project, forgeBin, "generate")
	store := readFileE2E(t, filepath.Join(db, "note_ext_store_gen.go"))
	if strings.Contains(store, "PrivateNote(") {
		t.Fatal("the ext store exposed the local complex64 type")
	}
	runCmd(t, project, "go", "build", "./...")
	runCmd(t, project, "go", "vet", "./internal/db")
}
