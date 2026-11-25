package config

import (
	"strings"
	"testing"
)

// database.migrations_dir moves the migrations, and the database is derived
// from the directory it names — not from db/migrations.
func TestMigrationsDir_DeclaredDirDerivesTheDatabase(t *testing.T) {
	root := t.TempDir()
	write(t, root, "sql/migrations/00001_init.up.sql", "CREATE TABLE t (id bigint primary key);\n")
	write(t, root, "forge.yaml", "name: demo\nmodule_path: example.com/demo\ndatabase:\n  migrations_dir: ./sql/migrations/\n")
	write(t, root, "go.mod", "module example.com/demo\n\ngo 1.24\n")
	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Database.MigrationsPath(); got != "sql/migrations" {
		t.Errorf("MigrationsPath = %q, want sql/migrations (cleaned)", got)
	}
	if cfg.Database.Driver != "postgres" {
		t.Errorf("Driver = %q, want postgres: the declared directory exists", cfg.Database.Driver)
	}
	if got := MigrationsDirIn(root); got != "sql/migrations" {
		t.Errorf("MigrationsDirIn = %q, want sql/migrations", got)
	}
}

// A declared directory that does not exist yet is kept, so the db commands
// name it instead of silently reading db/migrations.
func TestMigrationsDir_MissingDeclaredDirIsKept(t *testing.T) {
	root := t.TempDir()
	write(t, root, "db/migrations/00001_init.up.sql", "CREATE TABLE t (id bigint primary key);\n")
	write(t, root, "forge.yaml", "name: demo\nmodule_path: example.com/demo\ndatabase: {migrations_dir: migrations}\n")
	write(t, root, "go.mod", "module example.com/demo\n\ngo 1.24\n")
	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Database.Driver != "none" {
		t.Errorf("Driver = %q, want none: the declared directory does not exist (db/migrations is not it)", cfg.Database.Driver)
	}
	if got := cfg.Database.MigrationsPath(); got != "migrations" {
		t.Errorf("MigrationsPath = %q, want the declared migrations", got)
	}
	if got := MigrationsDirIn(root); got != "migrations" {
		t.Errorf("MigrationsDirIn (flow style) = %q, want migrations", got)
	}
}

func TestMigrationsDir_DefaultWhenUndeclared(t *testing.T) {
	root := serviceTree(t)
	if got := MigrationsDirIn(root); got != DefaultMigrationsDir {
		t.Errorf("MigrationsDirIn with no forge.yaml = %q, want %s", got, DefaultMigrationsDir)
	}
	cfg := loadTree(t, root)
	if got := cfg.Database.MigrationsPath(); got != DefaultMigrationsDir {
		t.Errorf("MigrationsPath = %q, want %s", got, DefaultMigrationsDir)
	}
	if got := MigrationsDirIn(root); got != DefaultMigrationsDir {
		t.Errorf("MigrationsDirIn = %q, want %s", got, DefaultMigrationsDir)
	}
}

func TestMigrationsDir_OutsideTheProjectIsRefused(t *testing.T) {
	for _, dir := range []string{"/srv/migrations", "../shared/migrations", ".."} {
		t.Run(dir, func(t *testing.T) {
			_, err := LoadProject([]byte("name: demo\nmodule_path: example.com/demo\ndatabase:\n  migrations_dir: "+dir+"\n"), "forge.yaml")
			ve := requireValidationError(t, err)
			if !strings.Contains(ve.Error(), "database.migrations_dir") || !strings.Contains(ve.Error(), "inside the project") {
				t.Errorf("want a refusal naming database.migrations_dir, got:\n%s", ve.Error())
			}
		})
	}
}

// A declared directory survives a forge.yaml rewrite; the derived default is
// never written back.
func TestMigrationsDir_NormalizeForWrite(t *testing.T) {
	root := t.TempDir()
	write(t, root, "migrations/00001_init.up.sql", "CREATE TABLE t (id bigint primary key);\n")
	write(t, root, "forge.yaml", "name: demo\nmodule_path: example.com/demo\ndatabase:\n  migrations_dir: migrations\n")
	write(t, root, "go.mod", "module example.com/demo\n\ngo 1.24\n")
	cfg, err := LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out, err := marshalNormalized(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "migrations_dir: migrations") {
		t.Errorf("a declared migrations_dir must survive the rewrite:\n%s", out)
	}

	def := loadTree(t, serviceTree(t))
	if def.Database.MigrationsPath() != DefaultMigrationsDir {
		t.Fatalf("MigrationsPath = %q, want %s", def.Database.MigrationsPath(), DefaultMigrationsDir)
	}
	out, err = marshalNormalized(def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "migrations_dir") {
		t.Errorf("the derived default must not be written back:\n%s", out)
	}
}
