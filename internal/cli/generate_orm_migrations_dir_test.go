package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forge bug 5d3c6d68: database.migrations_dir was advertised but the ORM
// projection read db/migrations regardless. With the key set, the projection
// applies the declared directory to the shadow database — and only it.
func TestORMProjectsTheDeclaredMigrationsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("applies the fixture's migrations to a shadow postgres; runs in task test")
	}
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":                           "module example.com/app\n\ngo 1.22\n",
		"forge.yaml":                       "name: app\nmodule_path: example.com/app\ndatabase:\n  migrations_dir: sql/migrations\n",
		"sql/migrations/00001_init.up.sql": "CREATE TABLE widgets (id TEXT PRIMARY KEY, label TEXT NOT NULL);\n",
		// A stale default directory must not be read.
		"db/migrations/00001_init.up.sql": "CREATE TABLE gadgets (id TEXT PRIMARY KEY);\n",
	} {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &pipelineContext{ProjectDir: dir, ModulePath: "example.com/app"}
	var planErr, ormErr error
	out := captureStdout(t, func() {
		planErr = stepPlanORMEntities(ctx)
		ormErr = stepInternalDBORM(ctx)
	})
	if planErr != nil || ormErr != nil || ctx.ORMFailure != nil {
		t.Fatalf("plan: %v / ORM: %v / %v\n%s", planErr, ormErr, ctx.ORMFailure, out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "internal/db/widget_orm_gen.go"))
	if err != nil {
		t.Fatalf("widgets (in sql/migrations) was not projected: %v\n%s", err, out)
	}
	if !strings.Contains(string(body), "of table widgets.") {
		t.Errorf("widget_orm_gen.go is not the projection of widgets:\n%s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal/db/gadget_orm_gen.go")); !os.IsNotExist(err) {
		t.Errorf("gadgets (in the undeclared db/migrations) was projected (stat: %v)", err)
	}
}
