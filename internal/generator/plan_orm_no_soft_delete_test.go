package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

func deletedAtTable(name, comment string) schemadef.Table {
	return schemadef.Table{
		Name:   name,
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.TypeString, IsPK: true, NotNull: true},
			{Name: "branch", Type: schemadef.TypeString, NotNull: true},
			{Name: "deleted_at", Type: schemadef.TypeTime, Comment: comment},
		},
	}
}

// From the column's catalog comment to the generated store: a deleted_at
// declared forge:no-soft-delete is a plain nullable timestamp — no Bun
// ,soft_delete tag, no forge soft-delete tag, no ListAll — while an
// undeclared one keeps soft delete. The declared half fails on the old code,
// which soft-deleted every time-typed deleted_at.
func TestGeneratePlanORM_NoSoftDeleteDeclarationReachesTheStore(t *testing.T) {
	root := t.TempDir()
	entities := []config.PlanEntity{
		codegen.EntityDefToPlanEntity(entityDefFromTable("Worktree", deletedAtTable("worktrees", "forge:no-soft-delete — the archive stamp"))),
		codegen.EntityDefToPlanEntity(entityDefFromTable("Note", deletedAtTable("notes", ""))),
	}
	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	read := func(file string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, "internal", "db", file))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(string(b)), " ")
	}

	worktree := read("worktree_orm_gen.go")
	if !strings.Contains(worktree, "DeletedAt *time.Time `bun:\"deleted_at\"`") {
		t.Errorf("a forge:no-soft-delete deleted_at must project as a plain nullable timestamp:\n%s", worktree)
	}
	for _, banned := range []string{"soft_delete", `forge:"soft-delete"`, "ListAllWorktree"} {
		if strings.Contains(worktree, banned) {
			t.Errorf("a forge:no-soft-delete entity's store carries %q", banned)
		}
	}

	note := read("note_orm_gen.go")
	if !strings.Contains(note, "`bun:\"deleted_at,soft_delete,nullzero\"`") || !strings.Contains(note, "func ListAllNote(") {
		t.Errorf("an undeclared deleted_at must stay soft delete:\n%s", note)
	}
}

// A legacy TEXT deleted_at under soft delete is DECLARED to pkg/crud with
// forge's own tag, because the repo no longer infers soft delete from a
// column's name.
func TestGeneratePlanORM_LegacyTextSoftDeleteIsTagged(t *testing.T) {
	root := t.TempDir()
	entities := []config.PlanEntity{{
		Name:       "Item",
		SoftDelete: true,
		Fields: []config.PlanEntityField{
			{Name: "id", Type: "string", PrimaryKey: true},
			{Name: "deleted_at", Type: "string"},
		},
	}}
	if err := GeneratePlanORM(root, config.ORMConfig{}, entities, nil); err != nil {
		t.Fatalf("GeneratePlanORM: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", "item_orm_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "`bun:\"deleted_at\" forge:\"soft-delete\"`") {
		t.Errorf("legacy TEXT deleted_at must carry forge:\"soft-delete\":\n%s", b)
	}
}
