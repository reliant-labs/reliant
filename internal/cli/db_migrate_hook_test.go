package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/pkg/migratekit"
)

// hookedDBProject is a -C project whose migrations a custom tool applies: a
// sh script that records the contract it was run with, so the test reads
// back what forge handed the tool.
func hookedDBProject(t *testing.T) (proj, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake migration tool is a sh script")
	}
	clearProjectDir(t)
	proj = writeProjectDirFixture(t, "hooked")
	record = filepath.Join(proj, "tool.log")
	body := "name: hooked\nmodule_path: github.com/example/hooked\ndatabase:\n  migrations:\n    dir: migrations\n    tool: custom\n" +
		"    apply: [sh, -c, 'echo \"apply $DATABASE_URL $FORGE_MIGRATIONS_DIR $FORGE_PHASE\" >> " + record + "']\n" +
		"    status: [sh, -c, 'echo \"status $DATABASE_URL\"']\n"
	if err := os.WriteFile(filepath.Join(proj, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere(t)
	if err := cmdutil.SetProjectDir(proj); err != nil {
		t.Fatal(err)
	}
	return proj, record
}

// `forge db migrate up` / `status` / `version` run the project's tool, with
// the database and the directory in its environment.
func TestDBMigrateRunsTheMigrationHook(t *testing.T) {
	proj, record := hookedDBProject(t)
	dir, err := requireMigrationsDir("")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(proj, "migrations") {
		t.Fatalf("migrations dir = %s, want the configured one", dir)
	}
	var out bytes.Buffer
	if err := runMigrateUpWith(&out, "postgres://x/app", dir, migratekit.Limits{}); err != nil {
		t.Fatalf("migrate up: %v\n%s", err, out.String())
	}
	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the tool did not run: %v\n%s", err, out.String())
	}
	if want := "apply postgres://x/app " + dir + " cli"; strings.TrimSpace(string(got)) != want {
		t.Errorf("the tool saw %q, want %q", strings.TrimSpace(string(got)), want)
	}
	if !strings.Contains(out.String(), "with custom") {
		t.Errorf("up should say which tool applied the migrations:\n%s", out.String())
	}

	for name, run := range map[string]func() error{
		"status":  func() error { return runMigrateStatus(&out, "postgres://x/app", dir) },
		"version": func() error { return runMigrateVersion(&out, "postgres://x/app", dir) },
	} {
		out.Reset()
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(out.String(), "status postgres://x/app") {
			t.Errorf("%s should print the tool's status:\n%s", name, out.String())
		}
	}
}

// The verbs that act on forge's migrator's state or file shape refuse,
// naming the tool.
func TestDBMigrationVerbsRefuseUnderTheMigrationHook(t *testing.T) {
	_, _ = hookedDBProject(t)
	dir, _ := requireMigrationsDir("")
	if err := runMigrateForce(&bytes.Buffer{}, "postgres://x/app", dir, "1"); err == nil || !strings.Contains(err.Error(), "custom applies this project's migrations") {
		t.Errorf("migrate force = %v; want a refusal naming the tool", err)
	}
	for _, args := range [][]string{
		{"migration", "new", "add_users"},
		{"migration", "rebase", "--all-pending"},
		{"squash"},
	} {
		root := newDBCmd()
		root.SetArgs(args)
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "database.migrations.tool") {
			t.Errorf("forge db %s = %v; want a refusal naming database.migrations.tool", strings.Join(args, " "), err)
		}
	}
}
