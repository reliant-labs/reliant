package migrationhook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func hookFor(t *testing.T, db config.DatabaseConfig) Hook {
	t.Helper()
	h, ok := Resolve(t.TempDir(), db)
	if !ok {
		t.Fatal("Resolve: no hook")
	}
	return h
}

func TestResolve_ForgeHasNoHook(t *testing.T) {
	if _, ok := Resolve(t.TempDir(), config.DatabaseConfig{}); ok {
		t.Error("tool forge must resolve to no hook")
	}
}

// TestPreset_PasswordNeverReachesTheArgv: the database URL carries the
// password and an argv is world-readable (/proc, ps). No built-in preset,
// expanded against a credentialed URL, carries the password: a tool that reads
// its connection from the environment gets no URL at all, and golang-migrate
// and atlas, which must be handed one, get it without the password, which
// travels as PGPASSWORD.
func TestPreset_PasswordNeverReachesTheArgv(t *testing.T) {
	const secret = "s3cr3t-pw"
	for _, dsn := range []string{
		"postgres://app:" + secret + "@db.internal:5432/app?sslmode=require",
		"postgres://app@db.internal:5432/app?password=" + secret + "&sslmode=require",
	} {
		for _, tool := range config.MigrationTools() {
			apply, status := Preset(tool)
			if len(apply) == 0 {
				continue
			}
			h := Hook{Tool: tool, Dir: "/m", ProjectDir: "/p"}
			vars, err := Vars(h, Invocation{DSN: dsn, Phase: PhaseCLI})
			if err != nil {
				t.Fatalf("%s: Vars: %v", tool, err)
			}
			for _, arg := range append(Expand(apply, vars), Expand(status, vars)...) {
				if strings.Contains(arg, secret) {
					t.Errorf("%s: the password reached the argv: %q", tool, arg)
				}
			}
			if vars["PGPASSWORD"] != secret {
				t.Errorf("%s: PGPASSWORD = %q, want the URL's password", tool, vars["PGPASSWORD"])
			}
			if got := vars["FORGE_DATABASE_URL_NO_PASSWORD"]; strings.Contains(got, secret) || !strings.Contains(got, "app@db.internal:5432/app") || !strings.Contains(got, "sslmode=require") {
				t.Errorf("%s: FORGE_DATABASE_URL_NO_PASSWORD = %q, want the URL with only its password removed", tool, got)
			}
		}
	}
	v, err := Vars(Hook{Tool: config.MigrationToolGoose, Dir: "/m"}, Invocation{DSN: "postgres://u:p@h/db"})
	if err != nil || v["GOOSE_DRIVER"] != "postgres" || v["GOOSE_DBSTRING"] != "postgres://u:p@h/db" {
		t.Errorf("goose's connection must come from GOOSE_DRIVER/GOOSE_DBSTRING: %v %v", v, err)
	}
	if v, _ := Vars(Hook{Tool: config.MigrationToolGoose}, Invocation{DSN: "postgres://u@h/db"}); hasKey(v, "PGPASSWORD") || v["FORGE_DATABASE_URL_NO_PASSWORD"] != "postgres://u@h/db" {
		t.Errorf("a URL with no password is passed through unchanged, and PGPASSWORD is left to the environment: %v", v)
	}
	// libpq's precedence: a password query parameter overrides the userinfo
	// password, even when it is empty.
	for dsn, want := range map[string]string{
		"postgres://u:userinfo-pw@h/db?password=query-pw": "query-pw",
		"postgres://u:userinfo-pw@h/db?password=":         "",
	} {
		v, err := Vars(Hook{Tool: config.MigrationToolGolangMigrate}, Invocation{DSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := v["PGPASSWORD"]; !ok || got != want {
			t.Errorf("%s: PGPASSWORD = %q (set %v), want %q", dsn, got, ok, want)
		}
		if got := v["FORGE_DATABASE_URL_NO_PASSWORD"]; got != "postgres://u@h/db" {
			t.Errorf("%s: FORGE_DATABASE_URL_NO_PASSWORD = %q, want postgres://u@h/db", dsn, got)
		}
	}
}

func TestResolve_PresetsAndOverrides(t *testing.T) {
	for _, tool := range config.MigrationTools() {
		if tool == config.MigrationToolForge || tool == config.MigrationToolCustom {
			continue
		}
		h := hookFor(t, config.DatabaseConfig{Migrations: config.MigrationsConfig{Tool: tool}})
		if len(h.Apply) == 0 || len(h.Status) == 0 {
			t.Errorf("%s: built-in tool has no default apply/status: %v / %v", tool, h.Apply, h.Status)
		}
	}
	h := hookFor(t, config.DatabaseConfig{Migrations: config.MigrationsConfig{Tool: "goose", Apply: []string{"make", "migrate"}}})
	if !reflect.DeepEqual(h.Apply, []string{"make", "migrate"}) || h.Status[0] != "goose" {
		t.Errorf("an apply override replaces the preset's apply only: %v / %v", h.Apply, h.Status)
	}
	if h.RelDir != config.DefaultMigrationsDir || h.Dir != filepath.Join(h.ProjectDir, "db", "migrations") {
		t.Errorf("dir = %q (%q), want the default", h.RelDir, h.Dir)
	}
}

func TestExpand_OnlyKnownBracedNames(t *testing.T) {
	got := Expand([]string{"-dir", "${FORGE_MIGRATIONS_DIR}/x", "${DATABASE_URL}", "$DATABASE_URL", "${HOME}", "p@$$w0rd"},
		map[string]string{"FORGE_MIGRATIONS_DIR": "/m", "DATABASE_URL": "postgres://h/db"})
	want := []string{"-dir", "/m/x", "postgres://h/db", "$DATABASE_URL", "${HOME}", "p@$$w0rd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Expand = %q, want %q", got, want)
	}
}

func TestVars_ToolEnv(t *testing.T) {
	dsn := "postgres://u:p%40ss@localhost:5432/app?sslmode=disable"
	h := hookFor(t, config.DatabaseConfig{Migrations: config.MigrationsConfig{Tool: "flyway"}})
	v, err := Vars(h, Invocation{DSN: dsn, Phase: PhaseShadow})
	if err != nil {
		t.Fatal(err)
	}
	if v["FLYWAY_URL"] != "jdbc:postgresql://localhost:5432/app?sslmode=disable" || v["FLYWAY_USER"] != "u" || v["FLYWAY_PASSWORD"] != "p@ss" {
		t.Errorf("flyway env = %v", v)
	}
	if v["FLYWAY_LOCATIONS"] != "filesystem:"+h.Dir || v["DATABASE_URL"] != dsn || v["FORGE_PHASE"] != PhaseShadow {
		t.Errorf("flyway env = %v", v)
	}
	g := hookFor(t, config.DatabaseConfig{Migrations: config.MigrationsConfig{Tool: "goose"}})
	v, _ = Vars(g, Invocation{DSN: dsn})
	if v["GOOSE_DBSTRING"] != dsn || v["GOOSE_DRIVER"] != "postgres" || v["GOOSE_MIGRATION_DIR"] != g.Dir {
		t.Errorf("goose env = %v", v)
	}
	if _, err := Vars(h, Invocation{DSN: "host=localhost dbname=app"}); err == nil {
		t.Error("flyway needs a URL DSN to build its JDBC URL")
	}
}

func TestDigest_TracksEveryFile(t *testing.T) {
	h := hookFor(t, config.DatabaseConfig{Migrations: config.MigrationsConfig{Tool: "goose"}})
	if d, n, err := Digest(h); err != nil || n != 0 || d != "" {
		t.Fatalf("missing dir: %q %d %v; want empty", d, n, err)
	}
	if err := os.MkdirAll(h.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.Dir, "00001_a.sql"), []byte("-- +goose Up\nCREATE TABLE a (id int);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d1, n, err := Digest(h)
	if err != nil || n != 1 {
		t.Fatalf("Digest: %d %v", n, err)
	}
	if err := os.WriteFile(filepath.Join(h.Dir, "00002_b.go"), []byte("package migrations\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d2, n, _ := Digest(h)
	if n != 2 || d2 == d1 {
		t.Errorf("a .go migration must change the digest (n=%d)", n)
	}
	h.Apply = []string{"make", "migrate"}
	if d3, _, _ := Digest(h); d3 == d2 {
		t.Error("the apply command is part of the digest")
	}
}

func TestApply_RunsTheCommandWithTheContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a sh script")
	}
	h := hookFor(t, config.DatabaseConfig{Migrations: config.MigrationsConfig{
		Tool:  "custom",
		Apply: []string{"sh", "-c", `echo "url=$DATABASE_URL dir=$FORGE_MIGRATIONS_DIR phase=$FORGE_PHASE arg=$0"`, "${DATABASE_URL}"},
	}})
	var out bytes.Buffer
	if err := Apply(context.Background(), h, Invocation{DSN: "postgres://x/y", Phase: PhaseCLI, Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	want := "url=postgres://x/y dir=" + h.Dir + " phase=cli arg=postgres://x/y"
	if strings.TrimSpace(out.String()) != want {
		t.Errorf("tool saw %q, want %q", out.String(), want)
	}

	h.Apply = []string{"sh", "-c", "exit 3"}
	if err := Apply(context.Background(), h, Invocation{DSN: "postgres://x/y"}); err == nil || !strings.Contains(err.Error(), "exited 3") {
		t.Errorf("a failing tool = %v; want its exit status", err)
	}
	h.Apply = []string{"forge-no-such-migration-tool"}
	if err := Apply(context.Background(), h, Invocation{}); err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("a missing tool = %v; want not found on PATH", err)
	}
	if err := Status(context.Background(), h, Invocation{}); err == nil || !strings.Contains(err.Error(), "no status command") {
		t.Errorf("custom with no status = %v; want it named", err)
	}
}

// Each built-in tool's history table is its ledger, at its default name or the
// one its argv names instead; the app's tables are not.
func TestLedgerTables(t *testing.T) {
	for _, tc := range []struct {
		tool          string
		apply         []string
		schema, table string
		want          bool
	}{
		{tool: config.MigrationToolGoose, schema: "public", table: "goose_db_version", want: true},
		{tool: config.MigrationToolGoose, apply: []string{"goose", "-table", "app_versions", "up"}, schema: "public", table: "app_versions", want: true},
		{tool: config.MigrationToolGoose, apply: []string{"goose", "-table=app_versions", "up"}, schema: "public", table: "app_versions", want: true},
		{tool: config.MigrationToolGoose, apply: []string{"goose", "-table", "public.app_versions", "up"}, schema: "public", table: "app_versions", want: true},
		{tool: config.MigrationToolGoose, apply: []string{"goose", "-table", "public.app_versions", "up"}, schema: "other", table: "app_versions"},
		{tool: config.MigrationToolGoose, apply: []string{"goose", "-table", "Hist.App_Versions", "up"}, schema: "hist", table: "app_versions", want: true},
		{tool: config.MigrationToolGoose, apply: []string{"goose", "-table", `"Hist"."Goose ""v"" .table"`, "up"}, schema: "Hist", table: `Goose "v" .table`, want: true},
		{tool: config.MigrationToolGoose, schema: "public", table: "widgets"},
		{tool: config.MigrationToolFlyway, schema: "public", table: "flyway_schema_history", want: true},
		{tool: config.MigrationToolFlyway, apply: []string{"flyway", "-table=history", "migrate"}, schema: "public", table: "history", want: true},
		{tool: config.MigrationToolAtlas, schema: "atlas_schema_revisions", table: "atlas_schema_revisions", want: true},
		{tool: config.MigrationToolAtlas, apply: []string{"atlas", "migrate", "apply", "--revisions-schema", "public"}, schema: "public", table: "atlas_schema_revisions", want: true},
		{tool: config.MigrationToolAtlas, schema: "public", table: "atlas_schema_revisions"},
		{tool: config.MigrationToolDbmate, apply: []string{"dbmate", "--migrations-table", "dbmate_versions", "migrate"}, schema: "public", table: "dbmate_versions", want: true},
		{tool: config.MigrationToolGolangMigrate, apply: []string{"migrate", "-database", "postgres://h/db?x-migrations-table=mig_versions&sslmode=disable", "up"}, schema: "public", table: "mig_versions", want: true},
		{tool: config.MigrationToolCustom, schema: "public", table: "goose_db_version"},
	} {
		h := Hook{Tool: tc.tool, Apply: tc.apply}
		if got := h.IsLedgerTable(tc.schema, tc.table); got != tc.want {
			t.Errorf("%s %v: IsLedgerTable(%s.%s) = %v, want %v", tc.tool, tc.apply, tc.schema, tc.table, got, tc.want)
		}
	}
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}
