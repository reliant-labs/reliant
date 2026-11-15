package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// migrateDirProject writes a project whose forge.yaml declares migrationsDir
// and puts one migration there.
func migrateDirProject(t *testing.T, migrationsDir string, grants bool) string {
	t.Helper()
	root := t.TempDir()
	put := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("forge.yaml", "name: p\nmodule_path: example.com/p\ndatabase:\n  migrations_dir: "+migrationsDir+"\n")
	put("go.mod", "module example.com/p\n\ngo 1.24\n")
	put(migrationsDir+"/00001_init.up.sql", "CREATE TABLE t (id bigint);\n")
	if grants {
		put("db/grants/readonly_gen.sql", "GRANT SELECT ON t TO forge_readonly;\n")
	}
	return root
}

func readGenerated(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// A directory one level under db/ is embedded by db/embed_gen.go itself, the
// way db/migrations always was.
func TestGenerateMigrate_DirUnderDB(t *testing.T) {
	root := migrateDirProject(t, "db/sql", true)
	if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
		t.Fatalf("GenerateMigrate: %v", err)
	}
	embed := readGenerated(t, root, "db/embed_gen.go")
	if !strings.Contains(embed, "//go:embed sql/*.sql grants\n") {
		t.Errorf("db/embed_gen.go must embed sql/*.sql and the grants:\n%s", embed)
	}
	src := readGenerated(t, root, "db/source_gen.go")
	if !strings.Contains(src, `const SourceDir = "sql"`) || !strings.Contains(src, "return MigrationsFS") {
		t.Errorf("db/source_gen.go must serve MigrationsFS at sql:\n%s", src)
	}
}

// A directory outside db/ gets an embed package of its own, which db.Source()
// mounts at SourceDir beside the grants.
func TestGenerateMigrate_DirOutsideDB(t *testing.T) {
	for _, grants := range []bool{false, true} {
		t.Run(map[bool]string{false: "no grants", true: "grants"}[grants], func(t *testing.T) {
			root := migrateDirProject(t, "sql/migrations", grants)
			if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
				t.Fatalf("GenerateMigrate: %v", err)
			}
			pkg := readGenerated(t, root, "sql/migrations/embed_gen.go")
			if !strings.Contains(pkg, "package migrations\n") || !strings.Contains(pkg, "//go:embed *.sql\nvar FS embed.FS") {
				t.Errorf("sql/migrations/embed_gen.go must embed the directory's SQL:\n%s", pkg)
			}
			src := readGenerated(t, root, "db/source_gen.go")
			for _, want := range []string{`migrations "example.com/p/sql/migrations"`, `const SourceDir = "migrations"`, "return sourceFS{}"} {
				if !strings.Contains(src, want) {
					t.Errorf("db/source_gen.go lacks %q:\n%s", want, src)
				}
			}
			_, err := os.Stat(filepath.Join(root, "db", "embed_gen.go"))
			if grants {
				if err != nil {
					t.Fatalf("db/embed_gen.go must embed the grants: %v", err)
				}
				if embed := readGenerated(t, root, "db/embed_gen.go"); !strings.Contains(embed, "//go:embed grants\nvar GrantsFS embed.FS") {
					t.Errorf("db/embed_gen.go must embed only the grants:\n%s", embed)
				}
				if !strings.Contains(src, "return GrantsFS.Open(name)") {
					t.Errorf("sourceFS must serve the grants from GrantsFS:\n%s", src)
				}
			} else if !os.IsNotExist(err) {
				t.Errorf("db/embed_gen.go must not exist with nothing in db/ to embed (stat: %v)", err)
			}
		})
	}
}

// setMigrationsDir rewrites forge.yaml's database.migrations_dir.
func setMigrationsDir(t *testing.T, root, dir string) {
	t.Helper()
	body := "name: p\nmodule_path: example.com/p\ndatabase:\n  migrations_dir: " + dir + "\n"
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNotExist(t *testing.T, root, rel, why string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Errorf("%s must be retired: %s (stat: %v)", rel, why, err)
	}
}

// Moving or emptying migrations_dir after a generation retires the embeds
// the earlier run wrote, which would otherwise embed SQL that is gone.
func TestGenerateMigrate_RetiresStaleEmbedsAfterTheDirChanges(t *testing.T) {
	t.Run("outside db, then under db", func(t *testing.T) {
		root := migrateDirProject(t, "sql/migrations", false)
		if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
			t.Fatalf("GenerateMigrate: %v", err)
		}
		setMigrationsDir(t, root, "db/migrations")
		if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
			t.Fatalf("GenerateMigrate after the move: %v", err)
		}
		assertNotExist(t, root, "sql/migrations/embed_gen.go", "db/ now embeds the SQL itself")
		if embed := readGenerated(t, root, "db/embed_gen.go"); !strings.Contains(embed, "//go:embed migrations/*.sql\n") {
			t.Errorf("db/embed_gen.go must embed migrations/*.sql:\n%s", embed)
		}
	})
	t.Run("outside db, then another dir outside db", func(t *testing.T) {
		root := migrateDirProject(t, "sql/migrations", false)
		if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
			t.Fatalf("GenerateMigrate: %v", err)
		}
		setMigrationsDir(t, root, "migrations")
		if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
			t.Fatalf("GenerateMigrate after the move: %v", err)
		}
		assertNotExist(t, root, "sql/migrations/embed_gen.go", "the package moved to migrations/")
		if src := readGenerated(t, root, "db/source_gen.go"); !strings.Contains(src, `migrations "example.com/p/migrations"`) {
			t.Errorf("db/source_gen.go must mount the new package:\n%s", src)
		}
	})
	for _, dir := range []string{"db/migrations", "sql/migrations"} {
		t.Run("migrations removed from "+dir, func(t *testing.T) {
			root := migrateDirProject(t, dir, true)
			if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
				t.Fatalf("GenerateMigrate: %v", err)
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(dir), "00001_init.up.sql")); err != nil {
				t.Fatal(err)
			}
			if err := GenerateMigrate(root, "example.com/p", false, nil); err != nil {
				t.Fatalf("GenerateMigrate with no SQL: %v", err)
			}
			assertNotExist(t, root, "db/embed_gen.go", "nothing is embedded with no SQL")
			assertNotExist(t, root, dir+"/embed_gen.go", "its *.sql matches nothing")
			if src := readGenerated(t, root, "db/source_gen.go"); !strings.Contains(src, "const HasMigrations = false") {
				t.Errorf("db/source_gen.go must report no migrations:\n%s", src)
			}
		})
	}
}

// An unembeddable directory is refused before its first migration, too.
func TestGenerateMigrate_UnembeddableDirIsRefusedWithoutMigrations(t *testing.T) {
	root := migrateDirProject(t, "_sql", false)
	err := GenerateMigrate(root, "example.com/p", false, nil)
	if err == nil || !strings.Contains(err.Error(), `"_sql"`) {
		t.Fatalf("GenerateMigrate = %v; want a refusal naming the directory the go tool ignores", err)
	}
}

func TestGenerateMigrate_UnembeddableDirIsRefused(t *testing.T) {
	root := migrateDirProject(t, "_sql", false)
	err := GenerateMigrate(root, "example.com/p", true, nil)
	if err == nil || !strings.Contains(err.Error(), `"_sql"`) {
		t.Fatalf("GenerateMigrate = %v; want a refusal naming the directory the go tool ignores", err)
	}
}

// The mounted filesystem is what the migrator reads: compile the generated
// files and walk Source() the way migratekit does (ReadDir of SourceDir, a
// migration by its joined path, the grants beside SourceDir).
func TestGenerateMigrate_MountedSourceServesTheMigrator(t *testing.T) {
	if testing.Short() {
		t.Skip("execs `go run` over a generated module; runs in task test")
	}
	root := migrateDirProject(t, "migrations", true)
	if err := os.WriteFile(filepath.Join(root, "db", "grants", "extra.sql"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateMigrate(root, "example.com/p", true, nil); err != nil {
		t.Fatalf("GenerateMigrate: %v", err)
	}
	main := `package main

import (
	"fmt"
	"io/fs"
	"path"

	"example.com/p/db"
)

func main() {
	entries, err := fs.ReadDir(db.Source(), db.SourceDir)
	if err != nil {
		panic(err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(db.Source(), path.Join(db.SourceDir, e.Name()))
		if err != nil {
			panic(err)
		}
		fmt.Printf("migration %s %q\n", e.Name(), b)
	}
	g, err := fs.ReadFile(db.Source(), path.Join(path.Dir(db.SourceDir), "grants/readonly_gen.sql"))
	if err != nil {
		panic(err)
	}
	fmt.Printf("grants %q\n", g)
}
`
	if err := os.MkdirAll(filepath.Join(root, "cmd", "probe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "probe", "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/probe")
	cmd.Dir = root
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	for _, want := range []string{`migration 00001_init.up.sql "CREATE TABLE t (id bigint);\n"`, `grants "GRANT SELECT ON t TO forge_readonly;\n"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("probe output lacks %q:\n%s", want, out)
		}
	}
}
