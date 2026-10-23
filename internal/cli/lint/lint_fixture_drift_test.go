package lint

import (
	"strings"
	"testing"
)

// The birth schema from the dogfood run that motivated this check, reduced
// to the two tables whose fixtures broke. At birth, total_cents is an
// ordinary column and nothing constrains it — which is exactly the schema the
// lifecycle test's seed block was written from.
const estimatesBirthSQL = `
CREATE TABLE estimates (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'SENT')),
    sent_at TIMESTAMPTZ,
    subtotal_cents BIGINT NOT NULL DEFAULT 0,
    tax_cents BIGINT NOT NULL DEFAULT 0,
    total_cents BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    estimate_id TEXT NOT NULL REFERENCES estimates (id),
    title TEXT NOT NULL
);
`

// The later migration that made total_cents derived — the change that turned
// every CRUD lifecycle test in two packages into a pq 428C9.
const totalCentsGeneratedSQL = `
ALTER TABLE estimates DROP COLUMN total_cents;
ALTER TABLE estimates ADD COLUMN total_cents BIGINT
    GENERATED ALWAYS AS (subtotal_cents + tax_cents) STORED NOT NULL;
`

// The later migration the db skill recommends for a lifecycle column: a
// one-way implication. No text matcher in the old lanes recognized it, so
// they reported clean over a fixture postgres rejects.
const sentHasStampSQL = `
ALTER TABLE estimates ADD CONSTRAINT estimates_sent_has_stamp
    CHECK (status <> 'SENT' OR sent_at IS NOT NULL);
`

// The later migration that made the estimate -> job edge one-to-one.
const jobEstimateUniqueSQL = `
ALTER TABLE jobs ADD CONSTRAINT jobs_estimate_id_key UNIQUE (estimate_id);
`

// staleFixtureGo is the scaffolded lifecycle test as forge wrote it BEFORE
// any later migration: the estimates column list names total_cents, the
// second estimate is SENT with no stamp, and the two jobs share an estimate.
//
// Written with explicit escapes rather than a raw literal because the file
// itself contains backticks — this is Go source embedding a SQL string.
const staleFixtureGo = "package handlers_test\n\n" +
	"// The estimates seed writes total_cents directly; see the factory for\n" +
	"// how the value is derived elsewhere.\n" +
	"func TestCRUD_Estimate_Lifecycle(t *testing.T) {\n" +
	"\tdb := crudTestDB(t)\n\n" +
	"\tif _, err := db.Exec(context.Background(), `\n" +
	"INSERT INTO \"estimates\" (\"id\", \"status\", \"subtotal_cents\", \"tax_cents\", \"total_cents\") VALUES\n" +
	"    ('est-1', 'DRAFT', 1000, 80, 1080),\n" +
	"    ('est-2', 'SENT', 2000, 160, 2160);\n" +
	"INSERT INTO \"jobs\" (\"id\", \"estimate_id\", \"title\") VALUES\n" +
	"    ('job-1', 'est-1', 'sample_title_1'),\n" +
	"    ('job-2', 'est-1', 'sample_title_2');\n" +
	"`); err != nil {\n" +
	"\t\tt.Fatalf(\"seed parent rows: %v\", err)\n" +
	"\t}\n" +
	"}\n"

const estimatesTestPath = "internal/handlers/estimates/handlers_crud_test.go"

// newEstimateProject lays down the project shape every direction of these
// tests shares: the birth migration, and the stale lifecycle test.
func newEstimateProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "db/migrations/20240101000000_create_estimates.up.sql", estimatesBirthSQL)
	writeFile(t, root, estimatesTestPath, staleFixtureGo)
	return root
}

// skipWithoutPostgres skips a test that executes fixtures against a shadow
// postgres under -short, like every other real-postgres test in the repo.
func skipWithoutPostgres(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("executes fixtures against a shadow postgres; skipped under -short")
	}
}

// collectOrFail runs the lane and fails the test on an engine error or an
// unverified run — every test below needs a real verdict.
func collectOrFail(t *testing.T, root string) fixtureDriftReport {
	t.Helper()
	rep, err := collectFixtureDriftFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collectFixtureDriftFindings: %v", err)
	}
	if rep.Unverified != "" {
		t.Fatalf("lane did not run: %s", rep.Unverified)
	}
	return rep
}

// TestFixtureDrift_CleanAgainstTheBirthSchema is the negative control for
// every rejection below: the same file against the schema it was written
// from executes cleanly, and the report says how much it executed.
func TestFixtureDrift_CleanAgainstTheBirthSchema(t *testing.T) {
	skipWithoutPostgres(t)
	rep := collectOrFail(t, newEstimateProject(t))
	if len(rep.Findings) != 0 {
		t.Fatalf("fixture written from this schema is reported rejected:\n%+v", rep.Findings)
	}
	if rep.Statements != 2 {
		t.Errorf("Statements = %d, want 2 (both INSERTs executed)", rep.Statements)
	}
}

// TestFixtureDrift_CheckAddedLaterIsRejected is the reproduction for the
// class the old text matchers missed entirely: a one-way status CHECK added
// after birth. Postgres's own verdict is the finding — constraint named, the
// migration that declared it attributed, the line of the statement to edit.
func TestFixtureDrift_CheckAddedLaterIsRejected(t *testing.T) {
	skipWithoutPostgres(t)
	root := newEstimateProject(t)
	writeFile(t, root, "db/migrations/20240401000000_sent_has_stamp.up.sql", sentHasStampSQL)

	rep := collectOrFail(t, root)
	if len(rep.Findings) == 0 {
		t.Fatal("a fixture the new CHECK rejects is reported clean")
	}
	f := rep.Findings[0]
	if f.Constraint != "estimates_sent_has_stamp" || f.Code != "23514" {
		t.Errorf("finding = constraint %q code %q, want estimates_sent_has_stamp / 23514 (check_violation)", f.Constraint, f.Code)
	}
	if f.DeclaredIn != "db/migrations/20240401000000_sent_has_stamp.up.sql" {
		t.Errorf("DeclaredIn = %q, want the migration that added the CHECK", f.DeclaredIn)
	}
	if f.File != estimatesTestPath || f.Table != `"estimates"` {
		t.Errorf("finding at %s (table %s), want %s / \"estimates\"", f.File, f.Table, estimatesTestPath)
	}
	wantLine := lineOf(staleFixtureGo, strings.Index(staleFixtureGo, `INSERT INTO "estimates"`))
	if f.Line != wantLine {
		t.Errorf("Line = %d, want %d (the INSERT the author edits)", f.Line, wantLine)
	}
	if !strings.Contains(fixtureDriftFixHint(f), "yours") || !strings.Contains(fixtureDriftFixHint(f), "factories_gen_test.go") {
		t.Errorf("fix hint must state ownership and point at the factories:\n%s", fixtureDriftFixHint(f))
	}
}

// TestFixtureDrift_GeneratedColumnIsRejected: a column made GENERATED after
// birth. Postgres refuses the whole statement (428C9); the finding carries
// that, attributed to the migration whose GENERATED clause names the column.
func TestFixtureDrift_GeneratedColumnIsRejected(t *testing.T) {
	skipWithoutPostgres(t)
	root := newEstimateProject(t)
	writeFile(t, root, "db/migrations/20240301000000_total_cents_generated.up.sql", totalCentsGeneratedSQL)

	rep := collectOrFail(t, root)
	var gen *fixtureDriftFinding
	for i := range rep.Findings {
		if rep.Findings[i].Code == "428C9" {
			gen = &rep.Findings[i]
		}
	}
	if gen == nil {
		t.Fatalf("no 428C9 finding for an INSERT naming a GENERATED column:\n%+v", rep.Findings)
	}
	if !strings.Contains(gen.Message, "total_cents") {
		t.Errorf("message %q does not name the column", gen.Message)
	}
	if gen.DeclaredIn != "db/migrations/20240301000000_total_cents_generated.up.sql" {
		t.Errorf("DeclaredIn = %q, want the migration that made it GENERATED", gen.DeclaredIn)
	}
}

// TestFixtureDrift_UniqueAndForeignKeyAreRejected: the two classes the old
// lanes each had a bespoke matcher for — a value repeated in a now-UNIQUE
// column, and (the crud-fixtures lane) a reference to a row nothing seeds —
// fall out of execution with no matcher at all.
func TestFixtureDrift_UniqueAndForeignKeyAreRejected(t *testing.T) {
	skipWithoutPostgres(t)
	root := newEstimateProject(t)
	writeFile(t, root, "db/migrations/20240501000000_job_estimate_unique.up.sql", jobEstimateUniqueSQL)
	dangling := strings.Replace(staleFixtureGo, "('job-2', 'est-1',", "('job-2', 'est-9',", 1)
	writeFile(t, root, estimatesTestPath, dangling)

	rep := collectOrFail(t, root)
	codes := map[string]bool{}
	for _, f := range rep.Findings {
		codes[f.Code] = true
	}
	// The jobs INSERT is ONE statement, so postgres reports the first
	// violation it meets; either is a correct verdict on that statement.
	if !codes["23503"] && !codes["23505"] {
		t.Fatalf("jobs fixture with a dangling reference under a new UNIQUE is reported clean:\n%+v", rep.Findings)
	}
}

// TestFixtureDrift_BlocksArePerTestFunction pins the grouping: each lifecycle
// test starts from a fresh database, so a parent seeded by one test function
// is NOT there for another. Executing the whole file in one transaction would
// report this fixture clean and the test would still fail.
func TestFixtureDrift_BlocksArePerTestFunction(t *testing.T) {
	skipWithoutPostgres(t)
	root := t.TempDir()
	writeFile(t, root, "db/migrations/20240101000000_create_estimates.up.sql", estimatesBirthSQL)
	writeFile(t, root, estimatesTestPath, "package handlers_test\n\n"+
		"func TestA(t *testing.T) {\n\tmustExec(t, `INSERT INTO estimates (id) VALUES ('est-1');`)\n}\n\n"+
		"func TestB(t *testing.T) {\n\tmustExec(t, `INSERT INTO jobs (id, estimate_id, title) VALUES ('job-1', 'est-1', 'x');`)\n}\n")

	rep := collectOrFail(t, root)
	if len(rep.Findings) != 1 || rep.Findings[0].Code != "23503" {
		t.Fatalf("TestB references a row only TestA seeds; want one FK finding, got:\n%+v", rep.Findings)
	}
}

// TestFixtureDrift_FactoryStyleTestOpensNoDatabase pins the cost contract: a
// lifecycle test that builds its rows from the factories carries no literal
// SQL, so the lane reports clean without applying a single migration — even
// pointed at a shadow server that does not exist.
func TestFixtureDrift_FactoryStyleTestOpensNoDatabase(t *testing.T) {
	t.Setenv("FORGE_TEST_POSTGRES_URL", "postgres://nobody:nothing@127.0.0.1:1/postgres?sslmode=disable")
	root := t.TempDir()
	writeFile(t, root, "db/migrations/20240101000000_create_estimates.up.sql", estimatesBirthSQL)
	writeFile(t, root, estimatesTestPath, "package handlers_test\n\n"+
		"func TestCRUD_Estimate_Lifecycle(t *testing.T) {\n"+
		"\tdb := crudTestDB(t)\n"+
		"\tfirst, err := svc.CreateEstimate(ctx, connect.NewRequest(estimates.NewCreateEstimateRequest(t, db, 0)))\n"+
		"\t_ = `SELECT count(*) FROM estimates`\n"+
		"}\n")
	rep, err := collectFixtureDriftFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collectFixtureDriftFindings: %v", err)
	}
	if rep.Unverified != "" || rep.Statements != 0 || len(rep.Findings) != 0 {
		t.Fatalf("a fixture-free test must cost nothing and report clean; got %+v", rep)
	}
}

// TestFixtureDrift_UnreachableShadowIsNotClean: literal fixtures that could
// not be executed are reported as NOT checked. A lane that read "could not
// look" as "clean" would be worse than no lane.
func TestFixtureDrift_UnreachableShadowIsNotClean(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the shadow server's connect retries; skipped under -short")
	}
	t.Setenv("FORGE_TEST_POSTGRES_URL", "postgres://nobody:nothing@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
	root := newEstimateProject(t)
	rep, err := collectFixtureDriftFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collectFixtureDriftFindings: %v", err)
	}
	if rep.Unverified == "" {
		t.Fatalf("an unreachable shadow must report the lane as not run; got %+v", rep)
	}
	js, err := collectFixtureDriftJSONAt(root, "db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(js) != 1 || js[0].Rule != fixtureDriftUnverifiedRule || js[0].Severity != lintSevWarning {
		t.Errorf("JSON must carry one %s warning, got %+v", fixtureDriftUnverifiedRule, js)
	}
}

// TestFixtureDrift_NoScaffoldedTestsIsClean / NoMigrations: ordinary states
// for a project this lane does not apply to.
func TestFixtureDrift_NoScaffoldedTestsIsClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "db/migrations/20240101000000_create_estimates.up.sql", estimatesBirthSQL)
	rep, err := collectFixtureDriftFindings(root, "db/migrations")
	if err != nil || rep.Unverified != "" || len(rep.Findings) != 0 {
		t.Fatalf("rep=%+v err=%v, want an empty report", rep, err)
	}
}

func TestFixtureDrift_NoMigrationsIsClean(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, estimatesTestPath, staleFixtureGo)
	rep, err := collectFixtureDriftFindings(root, "db/migrations")
	if err != nil || rep.Unverified != "" || len(rep.Findings) != 0 {
		t.Fatalf("rep=%+v err=%v, want an empty report", rep, err)
	}
}

// TestFixtureBlocks_Parsing pins what counts as fixture SQL: raw literals
// carrying an INSERT, split on statement-ending semicolons only, with
// bind-parameter statements skipped and line numbers pointing at each
// statement.
func TestFixtureBlocks_Parsing(t *testing.T) {
	src := "package x_test\n\n" +
		"// A comment with `INSERT INTO nope (a) VALUES (1);` in backticks is not a literal.\n" +
		"func TestX(t *testing.T) {\n" +
		"\texec(`\n" +
		"-- a comment; with a semicolon\n" +
		"INSERT INTO \"a\" (\"v\") VALUES ('x;y'), ('it''s');\n" +
		"INSERT INTO b (v) VALUES ($1);\n" +
		"UPDATE a SET v = 'z';\n" +
		"`)\n" +
		"\tquery(`SELECT 1; SELECT 2`)\n" +
		"}\n"
	blocks := fixtureBlocks("x_test.go", src)
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 (the SELECT literal carries no INSERT):\n%+v", len(blocks), blocks)
	}
	stmts := blocks[0].stmts
	if len(stmts) != 2 {
		t.Fatalf("got %d statements, want 2 (INSERT a + UPDATE; $1 skipped):\n%+v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0].sql, "('x;y'), ('it''s')") {
		t.Errorf("a semicolon inside a string literal split the statement: %q", stmts[0].sql)
	}
	if stmts[0].table != `"a"` || stmts[1].table != "" {
		t.Errorf("tables = %q, %q; want \"a\" and none", stmts[0].table, stmts[1].table)
	}
	if want := lineOf(src, strings.Index(src, `INSERT INTO "a"`)); stmts[0].line != want {
		t.Errorf("line = %d, want %d", stmts[0].line, want)
	}
	if want := lineOf(src, strings.Index(src, "UPDATE a")); stmts[1].line != want {
		t.Errorf("line = %d, want %d", stmts[1].line, want)
	}
	if fixtureBlocks("broken.go", "package x\nfunc {") != nil {
		t.Error("a file that does not parse must be skipped, not guessed at")
	}
}

// TestMigrationColumns_DropThenRecreateInOneFile pins the shared column
// replay the read-only rule depends on: a drop-then-recreate in one migration
// is the ordinary way to make a column GENERATED, and grouping every ADD
// before every DROP deleted the column outright.
func TestMigrationColumns_DropThenRecreateInOneFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "db/migrations/20240101000000_create.up.sql", estimatesBirthSQL)
	writeFile(t, root, "db/migrations/20240301000000_generated.up.sql", totalCentsGeneratedSQL)

	columns, err := readOnlyColumnsFromMigrations(root + "/db/migrations")
	if err != nil {
		t.Fatalf("readOnlyColumnsFromMigrations: %v", err)
	}
	col, ok := columns["estimates"]["total_cents"]
	if !ok {
		t.Fatal("estimates.total_cents missing after drop-then-recreate — " +
			"the ADD was applied before the DROP in the same file")
	}
	if !col.Generated {
		t.Errorf("estimates.total_cents Generated = false, want true\n%+v", col)
	}
}

// ── Report shape ──────────────────────────────────────────────────────────

func TestFixtureDriftJSON_SeverityIsWarning(t *testing.T) {
	skipWithoutPostgres(t)
	root := newEstimateProject(t)
	writeFile(t, root, "db/migrations/20240401000000_sent_has_stamp.up.sql", sentHasStampSQL)
	js, err := collectFixtureDriftJSONAt(root, "db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(js) == 0 {
		t.Fatal("no JSON findings for a rejected fixture")
	}
	for _, f := range js {
		if f.Severity != lintSevWarning || f.Rule != fixtureDriftRule {
			t.Errorf("finding %+v: want severity %s rule %s", f, lintSevWarning, fixtureDriftRule)
		}
	}
}
