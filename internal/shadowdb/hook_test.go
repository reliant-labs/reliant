package shadowdb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// fakeToolEnv makes this test binary act as a project's migration tool: the
// hook execs it (os.Args[0]) with the contract's environment, and TestMain
// hands control to fakeMigrationTool instead of the tests.
const fakeToolEnv = "FORGE_SHADOWDB_FAKE_MIGRATION_TOOL"

func TestMain(m *testing.M) {
	if os.Getenv(fakeToolEnv) == "1" {
		os.Exit(fakeMigrationTool())
	}
	os.Exit(m.Run())
}

// fakeMigrationTool is a minimal goose-shaped tool: it applies every *.sql
// in $FORGE_MIGRATIONS_DIR, in name order, to $DATABASE_URL — and refuses a
// file that says FAIL, the way a real tool reports a broken migration.
func fakeMigrationTool() int {
	if got := os.Getenv("FORGE_PHASE"); got != "shadow" {
		fmt.Fprintf(os.Stderr, "fake tool: FORGE_PHASE = %q, want shadow\n", got)
		return 2
	}
	db, err := sql.Open(pgtest.DriverName, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer func() { _ = db.Close() }()
	files, _ := filepath.Glob(filepath.Join(os.Getenv("FORGE_MIGRATIONS_DIR"), "*.sql"))
	sort.Strings(files)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		if strings.Contains(string(body), "FAIL") {
			fmt.Printf("fake tool: %s is broken\n", filepath.Base(f))
			return 1
		}
		if _, err := db.Exec(string(body)); err != nil {
			fmt.Fprintf(os.Stderr, "fake tool: %s: %v\n", filepath.Base(f), err)
			return 1
		}
	}
	return 0
}

// hookProject writes a project whose migrations — goose-named, so forge's own
// replay (*.up.sql) would find none — are applied by this binary as a custom
// migration tool, and points the shadow at the shared test server.
func hookProject(t *testing.T, files map[string]string) (dir, migDir string) {
	t.Helper()
	return hookProjectAs(t, "custom", nil, files)
}

// hookProjectAs is hookProject for a project that names tool, with this
// binary as its apply command followed by args.
func hookProjectAs(t *testing.T, tool string, args []string, files map[string]string) (dir, migDir string) {
	t.Helper()
	if testing.Short() {
		t.Skip("applies migrations to a real postgres through a subprocess; skipped under -short")
	}
	scratchURL, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	t.Setenv(pgtest.EnvBaseURL, scratchURL)
	t.Setenv(fakeToolEnv, "1")

	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir = t.TempDir()
	migDir = filepath.Join(dir, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	apply := []string{fmt.Sprintf("%q", self)}
	for _, a := range args {
		apply = append(apply, fmt.Sprintf("%q", a))
	}
	forgeYAML := fmt.Sprintf("name: p\nmodule_path: example.com/p\ndatabase:\n  migrations:\n    dir: migrations\n    tool: %s\n    apply: [%s]\n", tool, strings.Join(apply, ", "))
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(forgeYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, migDir
}

func tableNames(t *testing.T, dir, migDir string) []string {
	t.Helper()
	tables, err := Tables(dir, migDir)
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	var names []string
	for _, tb := range tables {
		names = append(names, tb.Name)
	}
	sort.Strings(names)
	return names
}

// The shadow every schema reader introspects is migrated by the project's own
// tool, inside a run and outside one, and a run applies it once.
func TestShadowAppliesThroughTheMigrationHook(t *testing.T) {
	dir, migDir := hookProject(t, map[string]string{
		"00001_widgets.sql": "CREATE TABLE widgets (id TEXT PRIMARY KEY, label TEXT NOT NULL)",
		"00002_gadgets.sql": "CREATE TABLE gadgets (id TEXT PRIMARY KEY, widget_id TEXT REFERENCES widgets(id))",
	})
	if got := strings.Join(tableNames(t, dir, migDir), ","); got != "gadgets,widgets" {
		t.Fatalf("outside a run: tables = %s, want gadgets,widgets", got)
	}

	end := BeginRun()
	defer end()
	before := CurrentStats()
	for range 2 {
		if got := strings.Join(tableNames(t, dir, migDir), ","); got != "gadgets,widgets" {
			t.Fatalf("inside a run: tables = %s, want gadgets,widgets", got)
		}
	}
	tables, shadow, err := Open(dir, migDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer shadow.Close()
	if len(tables) != 2 {
		t.Errorf("Open: %d tables, want 2", len(tables))
	}
	if _, err := shadow.DB().Exec("INSERT INTO widgets (id, label) VALUES ('w1', 'x')"); err != nil {
		t.Errorf("the opened shadow holds the hook-applied schema: %v", err)
	}
	if s := StatsSince(before); s.Applies != 1 {
		t.Errorf("a run applied the hook %d times, want once (%+v)", s.Applies, s)
	}
}

// A tool that fails fails the read, with the tool's own words.
func TestShadowHookFailureCarriesTheToolOutput(t *testing.T) {
	dir, migDir := hookProject(t, map[string]string{
		"00001_widgets.sql": "CREATE TABLE widgets (id TEXT PRIMARY KEY)",
		"00002_broken.sql":  "-- FAIL",
	})
	_, err := Tables(dir, migDir)
	if err == nil || !strings.Contains(err.Error(), "00002_broken.sql is broken") || !strings.Contains(err.Error(), "exited 1") {
		t.Fatalf("Tables = %v; want the tool's exit status and output", err)
	}
}

// Any directory other than the project's migrations directory is replayed by
// forge as before, even in a hooked project.
func TestShadowHookOnlyCoversTheProjectsMigrationsDir(t *testing.T) {
	dir, _ := hookProject(t, nil)
	other := filepath.Join(dir, "fixtures")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "00001_x.up.sql"), []byte("CREATE TABLE replayed (id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tableNames(t, dir, other), ","); got != "replayed" {
		t.Errorf("tables = %s, want replayed (forge's replay)", got)
	}
}

// The tool's own history table is its bookkeeping, not the app's schema: the
// shadow every reader introspects (the ORM, grants, mocks and the seed plan,
// whose reset truncates what it plans) never includes it, at its default name
// or the one the tool's argv names.
func TestShadowExcludesTheToolsLedger(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		ledger string
	}{
		{name: "default", ledger: "goose_db_version"},
		{name: "-table", args: []string{"-table", "app_versions"}, ledger: "app_versions"},
		{name: "schema-qualified -table", args: []string{"-table", "public.app_versions"}, ledger: "public.app_versions"},
		{name: "quoted, in its own schema", args: []string{"-table", `"Hist"."Goose Versions"`}, ledger: `"Hist"."Goose Versions"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, migDir := hookProjectAs(t, "goose", tc.args, map[string]string{
				"00000_ledger.sql":  `CREATE SCHEMA IF NOT EXISTS "Hist"; CREATE TABLE ` + tc.ledger + " (id serial PRIMARY KEY, version_id bigint NOT NULL, is_applied boolean NOT NULL)",
				"00001_widgets.sql": "CREATE TABLE widgets (id TEXT PRIMARY KEY)",
			})
			if got := strings.Join(tableNames(t, dir, migDir), ","); got != "widgets" {
				t.Fatalf("Tables = %s, want widgets alone (the %s ledger excluded)", got, tc.ledger)
			}
			tables, shadow, err := Open(dir, migDir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer shadow.Close()
			if len(tables) != 1 || tables[0].Name != "widgets" {
				t.Errorf("Open: tables = %v, want widgets alone", tables)
			}
		})
	}
}
