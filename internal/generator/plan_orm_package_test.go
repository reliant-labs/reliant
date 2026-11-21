package generator

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// database.orm.package (reliant P5b, F12.2): reliant's internal/db already
// declares 30 of the 55 names the projection would (`type Chat = core.Chat`,
// `Repo`, `Daemon`, …), so projecting there is refused (#713). The ORM gets a
// package of its own: every file lands there and declares it, and the
// collision check reads that package, not internal/db.
func TestGeneratePlanORM_LandsInTheConfiguredPackage(t *testing.T) {
	root := t.TempDir()
	// The project's own data layer declares Task — in internal/db, which is
	// no longer the ORM's package, so it is not a collision.
	writeDBFile(t, root, "models.go", "package db\n\ntype Task struct{ ID string }\n\ntype Store struct{}\n")

	orm := config.ORMConfig{Package: "internal/store"}
	if err := GeneratePlanORM(root, orm, []config.PlanEntity{taskPlanEntity()}, nil); err != nil {
		t.Fatalf("GeneratePlanORM into internal/store: %v", err)
	}
	for _, name := range []string{"task_orm_gen.go", "task_repo_ext.go", "store_gen.go"} {
		path := filepath.Join(root, "internal", "store", name)
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.Name.Name != "store" {
			t.Errorf("internal/store/%s declares package %s, want store", name, f.Name.Name)
		}
		if _, err := os.Stat(filepath.Join(root, "internal", "db", name)); err == nil {
			t.Errorf("internal/db/%s was written although the ORM package is internal/store", name)
		}
	}
}

// The refusal reads the ORM's own package and names it.
func TestGeneratePlanORM_CollisionIsCheckedInTheConfiguredPackage(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.go"), []byte("package store\n\ntype Task struct{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := GeneratePlanORM(root, config.ORMConfig{Package: "internal/store"}, []config.PlanEntity{taskPlanEntity()}, nil)
	var collision *ORMCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("want an *ORMCollisionError, got %v", err)
	}
	for _, want := range []string{"your internal/store already declares", "declared at internal/store/legacy.go:3", "forge declares it in internal/store/task_orm_gen.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q:\n%s", want, err)
		}
	}
}

// The default package's refusal offers the relocation.
func TestORMCollisionError_OffersTheORMPackage(t *testing.T) {
	root := t.TempDir()
	writeDBFile(t, root, "models.go", "package db\n\ntype Task struct{}\n")
	err := GeneratePlanORM(root, config.ORMConfig{}, []config.PlanEntity{taskPlanEntity()}, nil)
	if err == nil || !strings.Contains(err.Error(), "`database: {orm: {package: internal/store}}` in forge.yaml") {
		t.Fatalf("the refusal must offer database.orm.package; got %v", err)
	}
}

// Moving the ORM out of internal/db reclaims forge's previous projection
// there — pristine generated files only; the user's seams stay.
func TestGeneratePlanORM_MovingThePackageSweepsTheOldProjection(t *testing.T) {
	root := t.TempDir()
	cs := &FileChecksums{}
	if err := GeneratePlanORM(root, config.ORMConfig{}, []config.PlanEntity{taskPlanEntity()}, cs); err != nil {
		t.Fatal(err)
	}
	if err := GeneratePlanORM(root, config.ORMConfig{Package: "internal/store"}, []config.PlanEntity{taskPlanEntity()}, cs); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"task_orm_gen.go", "store_gen.go"} {
		if _, err := os.Stat(filepath.Join(root, "internal", "db", gone)); err == nil {
			t.Errorf("internal/db/%s survived the move to internal/store", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "db", "task_repo_ext.go")); err != nil {
		t.Errorf("the user-owned internal/db/task_repo_ext.go must survive the move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "store", "task_orm_gen.go")); err != nil {
		t.Errorf("internal/store/task_orm_gen.go: %v", err)
	}
}
