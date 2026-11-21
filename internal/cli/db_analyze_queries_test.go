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
	"github.com/reliant-labs/forge/pkg/querycapture"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const (
	queriesBaseSQL  = "CREATE TABLE orders (id int PRIMARY KEY, customer_id int, total int);\n"
	queriesIndexSQL = "-- forge:backward-compatible — new index\nCREATE INDEX CONCURRENTLY orders_customer ON orders (customer_id);\n"
)

// queriesProject is a git repository holding a Go module (example.com/app)
// with one migration and two repo files, committed as the base.
func queriesProject(t *testing.T) (projectDir, migDir string, git func(...string)) {
	t.Helper()
	testkit.Slow(t, "execs git to build a repository with a base commit, and boots postgres")
	projectDir, migDir = analyzeProject(t, map[string]string{"20260101000000_orders.up.sql": queriesBaseSQL})
	for name, body := range map[string]string{
		"go.mod":                   "module example.com/app\n\ngo 1.24\n",
		"internal/db/orders.go":    "package db\n",
		"internal/db/customers.go": "package db\n",
	} {
		p := filepath.Join(projectDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git = func(args ...string) {
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
	return projectDir, migDir, git
}

// writeCapture writes a capture file of records, each a statement run from a
// caller in the example.com/app module.
func writeCapture(t *testing.T, records map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for sql, file := range records {
		st := querycapture.Normalize(sql)
		rec := querycapture.Record{Version: querycapture.SchemaVersion, Fingerprint: st.Fingerprint, SQL: st.SQL,
			Operation: st.Operation, Tables: st.Tables, Test: "TestOrders",
			Caller: querycapture.Frame{File: file, Module: "example.com/app", Line: 12, Function: "example.com/app/internal/db.List"}}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "1-a.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const (
	byCustomerSQL = "SELECT * FROM orders WHERE customer_id = $1"
	byIDSQL       = "SELECT * FROM orders WHERE id = $1"
)

// Only the shapes whose caller changed since the base are planned; --all
// plans every one; nothing changed is a verdict with its reason.
func TestDBAnalyzeQueriesSelectsShapesWithChangedCallers(t *testing.T) {
	t.Parallel()
	projectDir, migDir, _ := queriesProject(t)
	capture := writeCapture(t, map[string]string{byCustomerSQL: "internal/db/orders.go", byIDSQL: "internal/db/customers.go"})
	opts := dbAnalyzeOptions{base: "HEAD", migDir: migDir, capture: capture, queriesOnly: true}

	doc := runDBAnalyze(context.Background(), projectDir, nil, opts, dsnDeps("", nil))
	if doc.Error != "" || doc.ExitCode != dbanalyze.ExitOK || doc.Summary.QueriesScanned != 0 || doc.Summary.QueriesCaptured != 2 ||
		!strings.Contains(doc.Summary.QueriesReason, "none of the 2 captured query shapes") {
		t.Fatalf("nothing changed: exit %d summary %+v error %q", doc.ExitCode, doc.Summary, doc.Error)
	}

	if err := os.WriteFile(filepath.Join(projectDir, "internal/db/orders.go"), []byte("package db\n\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc = runDBAnalyze(context.Background(), projectDir, nil, opts, dsnDeps("", nil))
	if doc.Error != "" {
		t.Fatal(doc.Error)
	}
	if len(doc.Queries) != 1 || doc.Queries[0].SQL != byCustomerSQL || !strings.HasPrefix(doc.Base, "HEAD@") {
		t.Fatalf("queries = %+v base %q", doc.Queries, doc.Base)
	}
	if len(doc.Migrations) != 0 || doc.Summary.QueriesScanned != 1 {
		t.Errorf("migrations %+v summary %+v", doc.Migrations, doc.Summary)
	}
	// Unknown size on a scratch target: a warning, which does not gate.
	if len(doc.Findings) != 1 || doc.Findings[0].Rule != dbanalyze.RuleQueryNoUsableIndex ||
		doc.Findings[0].Severity != dbanalyze.SeverityWarning || doc.Findings[0].File != "internal/db/orders.go" ||
		doc.Findings[0].Line != 12 || doc.ExitCode != dbanalyze.ExitOK {
		t.Errorf("exit %d findings %+v", doc.ExitCode, doc.Findings)
	}

	// A size hint makes it gate once the project's pin is past the rule's
	// warn-first release.
	cfg := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: "v99.0.0"}
	cfg.Database.Analyze.TableRows = map[string]int64{"orders": 1_000_000}
	allOpts := opts
	allOpts.all = true
	doc = runDBAnalyze(context.Background(), projectDir, cfg, allOpts, dsnDeps("", nil))
	if doc.Error != "" || len(doc.Queries) != 2 || doc.ExitCode != dbanalyze.ExitFindings {
		t.Fatalf("--all: exit %d queries %d error %q findings %+v", doc.ExitCode, len(doc.Queries), doc.Error, doc.Findings)
	}
	var buf strings.Builder
	if code := exitCodeOf(t, reportDBAnalyze(&buf, doc, false)); code != dbanalyze.ExitFindings {
		t.Errorf("reported exit %d", code)
	}
	for _, want := range []string{"internal/db/orders.go:12", "unserved (filter", "2 of 2 captured query shape(s) planned", dbanalyze.RuleQueryNoUsableIndex} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, buf.String())
		}
	}
}

// `forge db analyze --capture` runs the migration pass and then plans the
// queries on the schema it committed: the concurrent index build, which the
// migration pass runs for real, serves the plan.
func TestDBAnalyzeWithCaptureRunsBothPasses(t *testing.T) {
	t.Parallel()
	projectDir, migDir, _ := queriesProject(t)
	if err := os.WriteFile(filepath.Join(migDir, "20260102000000_customer_idx.up.sql"), []byte(queriesIndexSQL), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := writeCapture(t, map[string]string{byCustomerSQL: "internal/db/orders.go"})

	doc := runDBAnalyze(context.Background(), projectDir, nil,
		dbAnalyzeOptions{base: "HEAD", migDir: migDir, capture: capture, all: true}, dsnDeps("", nil))
	if doc.Error != "" {
		t.Fatal(doc.Error)
	}
	if len(doc.Migrations) != 1 || !doc.Migrations[0].Analyzed {
		t.Fatalf("migrations = %+v, want the index migration", doc.Migrations)
	}
	if len(doc.Queries) != 1 {
		t.Fatalf("queries = %+v", doc.Queries)
	}
	a := doc.Queries[0].Access
	if len(a) != 1 || a[0].Verdict != dbanalyze.AccessServed || a[0].Index != "orders_customer" {
		t.Errorf("access = %+v, want orders served by the new index", a)
	}
	if len(doc.Findings) != 0 || doc.ExitCode != dbanalyze.ExitOK || doc.Summary.MigrationsScanned != 1 || doc.Summary.QueriesScanned != 1 {
		t.Errorf("exit %d summary %+v findings %+v", doc.ExitCode, doc.Summary, doc.Findings)
	}
}

// On a dsn target the query pass alone copies the target, applies the
// pending migrations to the copy (the concurrent index build for real), and
// plans there; a shape that cannot be planned is a recorded skip, not a
// failure. The copy is gone afterwards, and the target never changed.
func TestDBAnalyzeQueriesOnADSNTargetAppliesPendingMigrations(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{
		"20260101000000_orders.up.sql":       queriesBaseSQL,
		"20260102000000_customer_idx.up.sql": queriesIndexSQL,
	})
	dsn := analyzeTarget(t, dbanalyze.MigrationFile{File: "20260101000000_orders.up.sql", Version: 20260101000000, SQL: queriesBaseSQL})
	capture := writeCapture(t, map[string]string{
		byCustomerSQL: "internal/db/orders.go",
		"SELECT * FROM my_tests_temp_table WHERE x = $1": "internal/db/orders_test.go",
	})
	doc := runDBAnalyze(context.Background(), projectDir, nil,
		dbAnalyzeOptions{target: "dsn", migDir: migDir, capture: capture, all: true, queriesOnly: true}, dsnDeps(dsn, nil))
	if doc.Error != "" {
		t.Fatal(doc.Error)
	}
	if len(doc.Migrations) != 0 || len(doc.Queries) != 2 || doc.Summary.QueriesScanned != 1 {
		t.Fatalf("migrations %d queries %d summary %+v", len(doc.Migrations), len(doc.Queries), doc.Summary)
	}
	for _, q := range doc.Queries {
		switch q.SQL {
		case byCustomerSQL:
			if len(q.Access) != 1 || q.Access[0].Index != "orders_customer" {
				t.Errorf("access = %+v, want the pending index", q.Access)
			}
		default:
			if q.SQLState != "42P01" {
				t.Errorf("temp table: %q %q", q.Error, q.SQLState)
			}
		}
	}
	skipped := false
	for _, s := range doc.Summary.Skipped {
		skipped = skipped || (strings.Contains(s.What, "orders_test.go") && strings.Contains(s.Reason, "could not be planned"))
	}
	if !skipped || doc.ExitCode != dbanalyze.ExitOK {
		t.Errorf("exit %d skipped %+v", doc.ExitCode, doc.Summary.Skipped)
	}
	if doc.Target.Clone == nil {
		t.Fatal("no copy reported")
	}
	if databaseExists(t, dsn, doc.Target.Clone.Database) {
		t.Errorf("the copy %s outlived the analysis", doc.Target.Clone.Database)
	}
	if databaseExists(t, dsn, "") {
		t.Error("the target gained the pending index: it was written")
	}

	// A pending migration that fails: the analysis cannot reach head, and
	// the copy is dropped on that path too.
	if err := os.WriteFile(filepath.Join(migDir, "20260103000000_broken.up.sql"), []byte("ALTER TABLE nope ADD COLUMN x int;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc = runDBAnalyze(context.Background(), projectDir, nil,
		dbAnalyzeOptions{target: "dsn", migDir: migDir, capture: capture, all: true, queriesOnly: true}, dsnDeps(dsn, nil))
	if doc.ExitCode != dbanalyze.ExitCouldNotAnalyze || !strings.Contains(doc.Error, "head schema") || doc.Target.Clone == nil {
		t.Fatalf("a failing pending migration: exit %d error %q", doc.ExitCode, doc.Error)
	}
	if databaseExists(t, dsn, doc.Target.Clone.Database) {
		t.Errorf("the copy %s outlived a failed analysis", doc.Target.Clone.Database)
	}
}

// databaseExists reports whether the server dsn names has database name;
// "" asks instead whether dsn's own database has the orders_customer index.
func databaseExists(t *testing.T, dsn, name string) bool {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	q, arg := "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name
	if name == "" {
		q, arg = "SELECT to_regclass($1) IS NOT NULL", "orders_customer"
	}
	var ok bool
	if err := conn.QueryRow(context.Background(), q, arg).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestDBAnalyzeQueriesNeedsACapture(t *testing.T) {
	t.Parallel()
	projectDir, migDir := analyzeProject(t, map[string]string{"20260101000000_orders.up.sql": queriesBaseSQL})
	for name, opts := range map[string]dbAnalyzeOptions{
		"queries without --capture": {migDir: migDir, queriesOnly: true},
		"--all without --capture":   {migDir: migDir, all: true},
		"a capture dir that is not": {migDir: migDir, queriesOnly: true, all: true, capture: filepath.Join(projectDir, "nope")},
	} {
		doc := runDBAnalyze(context.Background(), projectDir, nil, opts, dsnDeps("", nil))
		if doc.ExitCode != dbanalyze.ExitCouldNotAnalyze || doc.Error == "" {
			t.Errorf("%s: exit %d error %q", name, doc.ExitCode, doc.Error)
		}
	}
	// An empty capture directory is a verdict, not a failure.
	doc := runDBAnalyze(context.Background(), projectDir, nil,
		dbAnalyzeOptions{migDir: migDir, queriesOnly: true, all: true, capture: t.TempDir()}, dsnDeps("", nil))
	if doc.ExitCode != dbanalyze.ExitOK || !strings.Contains(doc.Summary.QueriesReason, "holds no query shapes") {
		t.Errorf("empty capture: exit %d summary %+v error %q", doc.ExitCode, doc.Summary, doc.Error)
	}
}

func TestFrameResolverResolvesSymlinksAboveMissingDirectories(t *testing.T) {
	t.Parallel()
	repo, alias := t.TempDir(), filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo, alias); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}
	root := realPath(repo)
	r := frameResolver{repoRoot: root, projectDir: root}
	frame := querycapture.Frame{AbsFile: filepath.Join(alias, "missing", "nested", "x.go")}
	if got, ok := r.repoPath(frame); !ok || got != "missing/nested/x.go" {
		t.Fatalf("resolve %q under %q: %q, %v", frame.AbsFile, root, got, ok)
	}
}

func TestFrameResolverMapsCapturedFramesIntoTheRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	r := frameResolver{repoRoot: realPath(repo), projectDir: realPath(filepath.Join(repo, "svc")), module: "example.com/svc"}
	for _, c := range []struct {
		f    querycapture.Frame
		want string
	}{
		{querycapture.Frame{File: "internal/db/x.go", Module: "example.com/svc"}, "svc/internal/db/x.go"},
		{querycapture.Frame{File: "internal/db/x.go"}, "svc/internal/db/x.go"},
		{querycapture.Frame{File: "internal/db/x.go", AbsFile: filepath.Join(repo, "lib", "y.go"), Module: "example.com/lib"}, "lib/y.go"},
		{querycapture.Frame{File: filepath.Join(repo, "missing", "nested", "x.go")}, "missing/nested/x.go"},
		{querycapture.Frame{AbsFile: "module/relative.go"}, ""},
		{querycapture.Frame{File: "pkg/orm/x.go", Module: "github.com/reliant-labs/forge"}, ""},
		{querycapture.Frame{File: "/elsewhere/x.go"}, ""},
	} {
		got, ok := r.repoPath(c.f)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%+v: %q %v, want %q", c.f, got, ok, c.want)
		}
	}
}
