package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const (
	analyzeBaseSQL = "CREATE TABLE orders (id int PRIMARY KEY, total int);\n"
	analyzeNewSQL  = "-- forge:backward-compatible — widening\nALTER TABLE orders ALTER COLUMN total TYPE bigint;\n"
)

// analyzeProject writes a project whose migrations directory holds files.
func analyzeProject(t *testing.T, files map[string]string) (projectDir, migDir string) {
	t.Helper()
	projectDir = t.TempDir()
	migDir = filepath.Join(projectDir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, sql := range files {
		if err := os.WriteFile(filepath.Join(migDir, name), []byte(sql), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir, migDir
}

// analyzeTarget is a database migrated with setup and marked as an analysis
// target, standing in for a snapshot a project points a dsn target at.
func analyzeTarget(t *testing.T, setup ...dbanalyze.MigrationFile) string {
	t.Helper()
	dsn := unmarkedAnalyzeTarget(t, setup...)
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(context.Background(),
		"DO $$ BEGIN EXECUTE format('ALTER DATABASE %I SET forge.analyze_target = %L', current_database(), 'on'); END $$"); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func unmarkedAnalyzeTarget(t *testing.T, setup ...dbanalyze.MigrationFile) string {
	t.Helper()
	testkit.Slow(t, "boots postgres (pkg/dbanalyze's own tests cover the mechanism under -short)")
	dsn, cleanup, err := dbanalyze.PrepareScratch(context.Background(), setup)
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

func dsnDeps(dsn string, declared map[string]string) analyzeDeps {
	return analyzeDeps{
		getenv: func(k string) string {
			if k == defaultAnalyzeDSNEnv {
				return dsn
			}
			return ""
		},
		declaredDSN: func(_ context.Context, _, env string) string { return declared[env] },
	}
}

// A dsn target analyzes exactly the migrations its schema_migrations lacks,
// and a forge rule's finding gates once the project's pin is past its
// warn-first release.
func TestDBAnalyzeDSNTargetAnalyzesWhatTheTargetLacks(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{
		"20260101000000_orders.up.sql": analyzeBaseSQL,
		"20260102000000_widen.up.sql":  analyzeNewSQL,
	})
	dsn := analyzeTarget(t, dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: analyzeBaseSQL})
	cfg := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: "v99.0.0"}

	doc := runDBAnalyze(context.Background(), projectDir, cfg, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps(dsn, nil))
	if doc.Error != "" {
		t.Fatalf("error: %s", doc.Error)
	}
	if len(doc.Migrations) != 1 || doc.Migrations[0].File != "db/migrations/20260102000000_widen.up.sql" {
		t.Fatalf("migrations = %+v, want the widen migration alone", doc.Migrations)
	}
	if doc.Target.Kind != dbanalyze.TargetDSN || doc.Target.ServerVersionNum == 0 || doc.Target.Identity == "" {
		t.Errorf("target = %+v", doc.Target)
	}
	if c := doc.Target.Clone; c == nil || !strings.HasPrefix(c.Database, "forge_analyze_") || c.SizeBytes <= 0 {
		t.Errorf("target.clone = %+v", c)
	}
	var rules []string
	for _, f := range doc.Findings {
		rules = append(rules, f.Rule+"/"+f.Severity)
	}
	if got := strings.Join(rules, ","); got != dbanalyze.RuleTableRewrite+"/error,"+dbanalyze.RuleAccessExclusiveLock+"/error" {
		t.Errorf("findings = %s", got)
	}
	if doc.ExitCode != dbanalyze.ExitFindings || doc.Summary.MigrationsScanned != 1 || doc.Summary.StatementsExecuted != 1 {
		t.Errorf("exit %d, summary %+v", doc.ExitCode, doc.Summary)
	}

	// The document is the contract: its shape round-trips under its schema.
	var buf strings.Builder
	err := reportDBAnalyze(&buf, doc, true)
	if code := exitCodeOf(t, err); code != dbanalyze.ExitFindings {
		t.Errorf("reported exit %d, want 1", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &decoded); err != nil {
		t.Fatal(err)
	}
	if c, ok := decoded["target"].(map[string]any)["clone"].(map[string]any); !ok || c["database"] == nil || c["duration_ms"] == nil || c["size_bytes"] == nil {
		t.Errorf("target.clone = %v", decoded["target"])
	}
	for _, key := range []string{"schema", "target", "base", "migrations", "queries", "findings", "summary", "exit_code"} {
		if _, ok := decoded[key]; !ok && key != "base" {
			t.Errorf("the document has no %q", key)
		}
	}
	if q, ok := decoded["queries"].([]any); !ok || len(q) != 0 {
		t.Errorf("queries = %v, want []", decoded["queries"])
	}
}

// A project pinned before the rules gate sees their findings as warnings.
func TestDBAnalyzeFindingsWarnFirst(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{
		"20260101000000_orders.up.sql": analyzeBaseSQL,
		"20260102000000_widen.up.sql":  analyzeNewSQL,
	})
	dsn := analyzeTarget(t, dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: analyzeBaseSQL})
	cfg := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: "v1.5.0"}
	doc := runDBAnalyze(context.Background(), projectDir, cfg, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps(dsn, nil))
	if doc.ExitCode != dbanalyze.ExitOK || len(doc.Findings) != 2 {
		t.Fatalf("exit %d findings %+v", doc.ExitCode, doc.Findings)
	}
	for _, f := range doc.Findings {
		if f.Severity != dbanalyze.SeverityWarning || !strings.Contains(f.Note, "gates from") {
			t.Errorf("%s: severity %s note %q", f.Rule, f.Severity, f.Note)
		}
	}
}

// An empty analysis is a verdict with a reason, never a silent pass.
func TestDBAnalyzeNothingPendingSaysSo(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{"20260101000000_orders.up.sql": analyzeBaseSQL})
	dsn := analyzeTarget(t, dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: analyzeBaseSQL})
	doc := runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps(dsn, nil))
	if doc.ExitCode != dbanalyze.ExitOK || doc.Summary.MigrationsScanned != 0 || !strings.Contains(doc.Summary.Reason, "applied every migration") {
		t.Errorf("exit %d summary %+v error %q", doc.ExitCode, doc.Summary, doc.Error)
	}
}

func TestDBAnalyzeCouldNotAnalyze(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{"20260101000000_orders.up.sql": analyzeBaseSQL})

	t.Run("no DSN", func(t *testing.T) {
		doc := runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps("", nil))
		if doc.ExitCode != dbanalyze.ExitCouldNotAnalyze || !strings.Contains(doc.Error, defaultAnalyzeDSNEnv) {
			t.Errorf("exit %d error %q", doc.ExitCode, doc.Error)
		}
	})
	t.Run("target ahead of the tree", func(t *testing.T) {
		dsn := analyzeTarget(t,
			dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: analyzeBaseSQL},
			dbanalyze.MigrationFile{File: "20260105000000_later.up.sql", Version: 20260105000000, SQL: "SELECT 1;"})
		doc := runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps(dsn, nil))
		if doc.ExitCode != dbanalyze.ExitCouldNotAnalyze || !strings.Contains(doc.Error, "ahead of the tree") {
			t.Errorf("exit %d error %q", doc.ExitCode, doc.Error)
		}
	})
	t.Run("unknown target kind", func(t *testing.T) {
		doc := runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{target: "prod", migDir: migDir}, dsnDeps("", nil))
		if doc.ExitCode != dbanalyze.ExitCouldNotAnalyze {
			t.Errorf("exit %d", doc.ExitCode)
		}
	})
}

// A target nobody marked is refused before anything is copied, with the line
// that marks it.
func TestDBAnalyzeRefusesAnUnmarkedTarget(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{
		"20260101000000_orders.up.sql": analyzeBaseSQL,
		"20260102000000_widen.up.sql":  analyzeNewSQL,
	})
	dsn := unmarkedAnalyzeTarget(t, dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: analyzeBaseSQL})
	doc := runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps(dsn, nil))
	if doc.ExitCode != dbanalyze.ExitCouldNotAnalyze || !strings.Contains(doc.Error, "SET forge.analyze_target = 'on'") || len(doc.Migrations) != 0 {
		t.Errorf("exit %d error %q migrations %d", doc.ExitCode, doc.Error, len(doc.Migrations))
	}
}

// The recommended safe patterns analyze clean end to end: a unique index
// built CONCURRENTLY and then attached as a constraint; an index dropped
// CONCURRENTLY and its name reused. And a type change of an indexed column
// is the rewrite it is, not an index built without CONCURRENTLY.
func TestDBAnalyzeConcurrentIndexPatterns(t *testing.T) {
	t.Parallel()
	const base = "CREATE TABLE orders (id int PRIMARY KEY, status varchar(10), email text);\nCREATE INDEX orders_status ON orders (status);\n"
	baseFile := dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: base}
	cases := []struct {
		name  string
		files map[string]string
		rules string
		exit  int
	}{
		{name: "unique index concurrently, then the constraint", exit: dbanalyze.ExitOK, files: map[string]string{
			"20260102000000_index.up.sql":  "CREATE UNIQUE INDEX CONCURRENTLY orders_email ON orders (email);\n",
			"20260103000000_attach.up.sql": "ALTER TABLE orders ADD CONSTRAINT orders_email_key UNIQUE USING INDEX orders_email;\n",
		}},
		{name: "drop concurrently, then reuse the name", exit: dbanalyze.ExitOK, files: map[string]string{
			"20260102000000_drop.up.sql":     "DROP INDEX CONCURRENTLY orders_status;\n",
			"20260103000000_recreate.up.sql": "CREATE INDEX CONCURRENTLY orders_status ON orders (status, id);\n",
		}},
		{name: "a type change of the primary key", exit: dbanalyze.ExitFindings,
			rules: dbanalyze.RuleTableRewrite + "," + dbanalyze.RuleAccessExclusiveLock,
			files: map[string]string{"20260102000000_widen.up.sql": "ALTER TABLE orders ALTER COLUMN id TYPE bigint;\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{baseFile.File: base}
			for k, v := range tc.files {
				files[k] = v
			}
			projectDir, migDir := analyzeProject(t, files)
			dsn := analyzeTarget(t, baseFile)
			cfg := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: "v99.0.0"}
			doc := runDBAnalyze(context.Background(), projectDir, cfg, dbAnalyzeOptions{target: "dsn", migDir: migDir}, dsnDeps(dsn, nil))
			var rules []string
			for _, f := range doc.Findings {
				rules = append(rules, f.Rule)
			}
			if doc.ExitCode != tc.exit || strings.Join(rules, ",") != tc.rules {
				t.Errorf("exit %d (error %q), findings %v; want exit %d, %q", doc.ExitCode, doc.Error, rules, tc.exit, tc.rules)
			}
		})
	}
}

// A DSN a non-development environment declares is refused; a dev
// environment's, or one no environment declares, is not.
func TestDBAnalyzeRefusesADeployedEnvironmentsDatabase(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	for env, mode := range map[string]string{"dev": "development", "prod": "production"} {
		dir := filepath.Join(projectDir, "deploy", "kcl", env)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, "main.k"), []byte("x = 1\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "config.k"), []byte("environment = \""+mode+"\"\n"), 0o644)
	}
	declared := map[string]string{
		"dev":  "postgres://u:p@localhost:5432/app_dev",
		"prod": "postgres://app:secret@db.internal:5432/app",
	}
	lookup := func(_ context.Context, _, env string) string { return declared[env] }
	ctx := context.Background()

	err := refuseDeployedAnalyzeDSN(ctx, projectDir, "postgres://reader:x@db.internal/app?sslmode=require", lookup)
	if err == nil || !strings.Contains(err.Error(), `environment "prod"`) || strings.Contains(err.Error(), "secret") {
		t.Errorf("prod's database: err = %v", err)
	}
	for _, ok := range []string{"postgres://u:p@127.0.0.1:5432/app_dev", "postgres://u:p@snapshots.internal:5432/app"} {
		if err := refuseDeployedAnalyzeDSN(ctx, projectDir, ok, lookup); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// Judged by where pgx connects: a snapshot's authority with routing
	// parameters that point at prod, or prod spelled as keyword/value, is
	// prod's database.
	for _, dsn := range []string{
		"postgres://u:p@snapshots.internal:5432/app?host=db.internal",
		"postgres://u:p@db.internal:6543/app?port=5432",
		"postgres://u:p@db.internal:5432/snapshot?dbname=app",
		"host=db.internal dbname=app user=reader password=secret",
	} {
		err := refuseDeployedAnalyzeDSN(ctx, projectDir, dsn, lookup)
		if err == nil || !strings.Contains(err.Error(), `environment "prod"`) || strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: err = %v", dsn, err)
		}
	}
	// Several hosts have no single identity: refused, whichever they are.
	if err := refuseDeployedAnalyzeDSN(ctx, projectDir, "postgres://u:p@snapshots.internal:5432,db.internal:5432/app", lookup); err == nil {
		t.Error("a multi-host DSN was accepted")
	}
	if err := refuseDeployedAnalyzeDSN(ctx, projectDir, "host=snapshots.internal", lookup); err == nil {
		t.Error("a DSN naming no database was accepted")
	}
	// A declared multi-host DSN refuses each of its hosts.
	multi := func(_ context.Context, _, env string) string {
		if env == "prod" {
			return "postgres://app@db-a.internal:5432,db-b.internal:5432/app"
		}
		return ""
	}
	if err := refuseDeployedAnalyzeDSN(ctx, projectDir, "postgres://u@db-b.internal/app", multi); err == nil {
		t.Error("the second host of prod's multi-host DSN was accepted")
	}
}

func TestDBAnalyzePendingOnTarget(t *testing.T) {
	t.Parallel()
	ms := []compat.Migration{{Version: 1}, {Version: 2}, {Version: 3}}
	files := []dbanalyze.MigrationFile{{Version: 1}, {Version: 2}, {Version: 3}}
	versions := func(fs []dbanalyze.MigrationFile) (out []uint64) {
		for _, f := range fs {
			out = append(out, f.Version)
		}
		return out
	}
	if got, err := pendingOnTarget(ms, files, dbanalyze.MigrationState{}); err != nil || len(got) != 3 {
		t.Errorf("unmigrated: %v %v", versions(got), err)
	}
	if got, err := pendingOnTarget(ms, files, dbanalyze.MigrationState{Applied: true, Version: 2}); err != nil || len(got) != 1 || got[0].Version != 3 {
		t.Errorf("at 2: %v %v", versions(got), err)
	}
	for _, st := range []dbanalyze.MigrationState{
		{Applied: true, Version: 4},
		{Applied: true, Version: 2, Dirty: true},
		{Applied: true, Version: 0},
	} {
		if _, err := pendingOnTarget(ms[1:], files[1:], st); err == nil {
			t.Errorf("%+v: no error", st)
		}
	}
}

// The scratch target end to end: the migration added since --base is
// analyzed, the one already at base is setup.
func TestDBAnalyzeScratchSplitsAtBase(t *testing.T) {
	testkit.Slow(t, "execs git to build a repository with a base commit")
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{"20260101000000_orders.up.sql": analyzeBaseSQL})
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = projectDir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	if err := os.WriteFile(filepath.Join(migDir, "20260102000000_widen.up.sql"), []byte(analyzeNewSQL), 0o644); err != nil {
		t.Fatal(err)
	}

	doc := runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{base: "HEAD", migDir: migDir}, dsnDeps("", nil))
	if doc.Error != "" {
		t.Fatal(doc.Error)
	}
	if !strings.HasPrefix(doc.Base, "HEAD@") || doc.Target.Kind != dbanalyze.TargetScratch || doc.Target.HasStatistics {
		t.Errorf("base %q target %+v", doc.Base, doc.Target)
	}
	if len(doc.Migrations) != 1 || !strings.HasSuffix(doc.Migrations[0].File, "_widen.up.sql") {
		t.Fatalf("migrations = %+v", doc.Migrations)
	}
	if len(doc.Findings) != 2 {
		t.Errorf("findings = %+v", doc.Findings)
	}

	git("add", ".")
	git("commit", "-q", "-m", "widen")
	doc = runDBAnalyze(context.Background(), projectDir, nil, dbAnalyzeOptions{base: "HEAD", migDir: migDir}, dsnDeps("", nil))
	if doc.ExitCode != dbanalyze.ExitOK || doc.Summary.MigrationsScanned != 0 || !strings.Contains(doc.Summary.Reason, "added or changed since HEAD@") {
		t.Errorf("nothing changed: exit %d summary %+v error %q", doc.ExitCode, doc.Summary, doc.Error)
	}
}
