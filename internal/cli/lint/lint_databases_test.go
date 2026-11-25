package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// `forge lint --migration-safety` covers EVERY database the env declaration
// declares, each under its own dials, and names the database on each finding
// when there are several.
func TestMigrationSafety_EveryDeclaredDatabase(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: x\nmodule_path: example.com/x\n")
	write("db/migrations/20260101000000_drop.up.sql", "DROP TABLE accounts;\n")
	write("analytics/migrations/20260101000000_drop.up.sql", "DROP TABLE events;\n")
	t.Chdir(dir)

	dbs := []config.DeclaredDatabase{
		{Name: "analytics", Source: config.DatabaseSourceKCL, Migrations: config.MigrationsConfig{Dir: "analytics/migrations"},
			// This database's dials downgrade the destructive change.
			MigrationSafety: config.MigrationSafetyConfig{DestructiveChange: "warn"}},
		{Name: "default", Source: config.DatabaseSourceKCL, Migrations: config.MigrationsConfig{Dir: "db/migrations"}},
	}
	prev := config.RegisterDatabaseDeclarer(func(string) ([]config.DeclaredDatabase, error) { return dbs, nil })
	t.Cleanup(func() { config.RegisterDatabaseDeclarer(prev) })

	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	findings, hasErrors, err := collectMigrationSafetyJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !hasErrors {
		t.Error("the default database's DROP TABLE must gate")
	}
	var analytics, primary *lintJSONFinding
	for i, f := range findings {
		switch {
		case strings.HasPrefix(f.Message, "database analytics: "):
			analytics = &findings[i]
		case strings.HasPrefix(f.Message, "database default: "):
			primary = &findings[i]
		}
	}
	if analytics == nil || primary == nil {
		t.Fatalf("want one finding per database, prefixed with its name; got %+v", findings)
	}
	if !strings.Contains(analytics.File, filepath.Join("analytics", "migrations")) || analytics.Severity != "warning" {
		t.Errorf("analytics finding = %+v; want a warning in analytics/migrations (its own dials)", *analytics)
	}
	if !strings.Contains(primary.File, filepath.Join("db", "migrations")) || primary.Severity != "error" {
		t.Errorf("default finding = %+v; want an error in db/migrations", *primary)
	}
	if filepath.IsAbs(primary.File) {
		t.Errorf("finding file %q is absolute; want it relative to the project root, as the single-database pass reports it", primary.File)
	}

	// One declared database: no prefix. It declares no tool and the project
	// no driver, so forge's own migrator applies it and forge's rules gate
	// it: the migration-safety pass is never skipped for the default
	// declaration (review R5.B1 on #2211).
	dbs = dbs[1:]
	if got := dbs[0].Config(config.DatabaseConfig{}).MigrationTool(); got != config.MigrationToolForge {
		t.Fatalf("a KCL database with no tool resolves to migration tool %q, want %q", got, config.MigrationToolForge)
	}
	findings, hasErrors, err = collectMigrationSafetyJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !hasErrors {
		t.Errorf("one KCL database with the default tool: its DROP TABLE must gate; findings %+v", findings)
	}
	for _, f := range findings {
		if strings.Contains(strings.ToLower(f.Message), "skip") {
			t.Errorf("the default declaration's migrations were skipped: %q", f.Message)
		}
		if strings.HasPrefix(f.Message, "database ") {
			t.Errorf("a single database's finding is prefixed: %q", f.Message)
		}
	}
}
