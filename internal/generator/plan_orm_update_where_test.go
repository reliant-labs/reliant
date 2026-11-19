package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func readDB(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "internal", "db", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// forge#2266, option 1: a set-based write is generated beside the per-row
// ones, as a package-level function over crud.Repo.UpdateWhere. The store
// interface gains no method (a hand-written store implementation keeps
// compiling), an entity with no write path gets none, and a project that
// already declares the name keeps its own instead of a collision refusal.
func TestGeneratePlanORM_SetBasedUpdateWhere(t *testing.T) {
	root := t.TempDir()
	if err := GeneratePlanORM(root, config.ORMConfig{}, widgetEntity(), nil); err != nil {
		t.Fatal(err)
	}
	orm := readDB(t, root, "widget_orm_gen.go")
	if !strings.Contains(orm, "func UpdateWidgetWhere(ctx context.Context, db orm.Context, values *Widget, fields []string, filters ...orm.QueryOption) (int64, error) {\n\treturn widgetRepo.UpdateWhere(ctx, db, values, fields, filters...)\n}") {
		t.Errorf("widget_orm_gen.go lacks UpdateWidgetWhere:\n%s", orm)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "internal", "db"))
	for _, e := range entries {
		if strings.Contains(e.Name(), "store") && strings.Contains(readDB(t, root, e.Name()), "UpdateWidgetWhere") {
			t.Errorf("%s names UpdateWidgetWhere: a store interface must not gain a method (semver 1)", e.Name())
		}
	}

	// Append-only: no write path, no set-based write.
	ao := t.TempDir()
	if err := GeneratePlanORM(ao, config.ORMConfig{}, appendOnlyStoreEntities(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readDB(t, ao, "payment_orm_gen.go"), "UpdatePaymentWhere") {
		t.Error("an append-only entity got a set-based update")
	}

	// Version-checked: crud.Repo.UpdateWhere refuses it (forge#2335 [B1]), so
	// no delegate that always fails, and the seam says why.
	vr := t.TempDir()
	versioned := widgetEntity()
	versioned[0].Fields = append(versioned[0].Fields, config.PlanEntityField{Name: "version", Type: "int64", NotNull: true, Version: true})
	if err := GeneratePlanORM(vr, config.ORMConfig{}, versioned, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readDB(t, vr, "widget_orm_gen.go"), "UpdateWidgetWhere") {
		t.Error("a forge:version entity got a set-based update, which crud.Repo.UpdateWhere refuses")
	}
	seam := readDB(t, vr, "widget_repo_ext.go")
	if !strings.Contains(seam, "there is no\n// generated UpdateWidgetWhere") || !strings.Contains(seam, `Set("version = version + 1")`) {
		t.Errorf("the versioned seam header does not say there is no UpdateWidgetWhere and advance the version:\n%s", seam)
	}

	// A project that wrote its own keeps it.
	own := t.TempDir()
	writeDBFile(t, own, "widget_repo_ext.go", "package db\n\nimport \"context\"\n\n// UpdateWidgetWhere is mine.\nfunc UpdateWidgetWhere(ctx context.Context) error { return nil }\n")
	if err := GeneratePlanORM(own, config.ORMConfig{}, widgetEntity(), nil); err != nil {
		t.Fatalf("a project declaring UpdateWidgetWhere must still generate: %v", err)
	}
	if strings.Contains(readDB(t, own, "widget_orm_gen.go"), "func UpdateWidgetWhere") {
		t.Error("forge redeclared the project's UpdateWidgetWhere")
	}
	if !strings.Contains(readDB(t, own, "widget_repo_ext.go"), "is mine") {
		t.Error("the project's own declaration was touched")
	}
}
