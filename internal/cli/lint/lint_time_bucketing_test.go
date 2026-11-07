package lint

import (
	"path/filepath"
	"strings"
	"testing"
)

// The schema shape the dogfood run's fleet-telemetry app had: a telemetry
// table whose sample time is a TIMESTAMPTZ (which is the RIGHT column type),
// alongside a column that is deliberately TIMESTAMP WITHOUT TIME ZONE — a
// wall-clock field with no session dependence at all.
const telemetryBirthSQL = `
CREATE TABLE telemetry_samples (
    id TEXT PRIMARY KEY,
    recorded_at TIMESTAMPTZ NOT NULL,
    shift_started_at TIMESTAMP NOT NULL,
    distance_m BIGINT NOT NULL DEFAULT 0
);
`

// newTelemetryProject lays down the migrations every direction of these
// tests shares.
func newTelemetryProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "db/migrations/20240101000000_create_telemetry.up.sql", telemetryBirthSQL)
	return root
}

// goHandlerWith wraps a SQL fragment in the shape a handler's query constant
// actually has: a raw backtick literal inside a Go file.
func goHandlerWith(sql string) string {
	return "package handlers\n\nconst dailyDistanceQuery = `" + sql + "`\n"
}

// ── The firing case ───────────────────────────────────────────────────────

// A two-argument date_trunc over a TIMESTAMPTZ column is the whole point of
// the rule: it truncates in the SESSION timezone, which pgx sets from the
// client host, so the same rows bucket differently on a UTC CI box and a
// UTC-5 laptop. Verified against real postgres in
// pkg/pgtest/datetrunc_premise_pg_test.go.
func TestTimeBucketing_TwoArgOverTimestamptz_Fires(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/queries.go", goHandlerWith(`
SELECT date_trunc('day', recorded_at) AS bucket, sum(distance_m)
FROM telemetry_samples GROUP BY 1 ORDER BY 1`))

	findings, err := collectTimeBucketFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	f := findings[0]
	// File is the native project-relative path (backslashed on Windows).
	if filepath.ToSlash(f.File) != "internal/handlers/telemetry/queries.go" {
		t.Errorf("file = %q", f.File)
	}
	if f.Line != 4 {
		t.Errorf("line = %d, want 4", f.Line)
	}
	if f.Col == 0 {
		t.Errorf("col = 0, want the offset of the date_trunc call")
	}
	if f.Column != "recorded_at" {
		t.Errorf("column = %q, want recorded_at", f.Column)
	}
	if f.Unit != "day" {
		t.Errorf("unit = %q, want day", f.Unit)
	}
	if f.Table != "telemetry_samples" {
		t.Errorf("table = %q, want telemetry_samples", f.Table)
	}

	// The message has to carry the literal fix and say what goes wrong,
	// not merely that something is wrong.
	hint := timeBucketFixHint(f)
	for _, want := range []string{
		"date_trunc('day', recorded_at, 'UTC')",
		"SESSION",
		"host",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// A view in a migration is project SQL too, and it is where a reporting
// query most often lives once it stops being ad-hoc.
func TestTimeBucketing_InMigrationView_Fires(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "db/migrations/20240202000000_daily_view.up.sql", `
CREATE VIEW daily_distance AS
SELECT date_trunc('week', recorded_at) AS bucket, sum(distance_m)
FROM telemetry_samples GROUP BY 1;
`)
	findings, err := collectTimeBucketFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(findings), findings)
	}
	if findings[0].Unit != "week" {
		t.Errorf("unit = %q, want week", findings[0].Unit)
	}
	if !strings.HasSuffix(findings[0].File, "20240202000000_daily_view.up.sql") {
		t.Errorf("file = %q", findings[0].File)
	}
}

// ── The non-firing cases ──────────────────────────────────────────────────

// The three-argument form IS the fix. Firing on it would tell the author to
// undo the correct thing, which is how a rule earns a blanket disable.
func TestTimeBucketing_ThreeArgForm_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/queries.go", goHandlerWith(`
SELECT date_trunc('day', recorded_at, 'UTC') AS bucket FROM telemetry_samples`))
	assertNoTimeBucketFindings(t, root)
}

// A deliberate local-time requirement is spelled the same way, with a named
// zone rather than UTC. Also silent — the author has stated the zone, which
// is the entire thing the rule asks for.
func TestTimeBucketing_ThreeArgNamedZone_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/queries.go", goHandlerWith(`
SELECT date_trunc('day', recorded_at, 'America/New_York') FROM telemetry_samples`))
	assertNoTimeBucketFindings(t, root)
}

// A TIMESTAMP WITHOUT TIME ZONE column has no session dependence: postgres
// truncates the stored wall-clock value, and the answer is the same on every
// host. Confirmed against real postgres alongside the firing case.
func TestTimeBucketing_TimestampWithoutZone_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/queries.go", goHandlerWith(`
SELECT date_trunc('day', shift_started_at) FROM telemetry_samples`))
	assertNoTimeBucketFindings(t, root)
}

// A column this check cannot resolve to a declared type is a gap in the
// parser, not evidence about the app. Precision over coverage.
func TestTimeBucketing_UnresolvableColumn_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/queries.go", goHandlerWith(`
SELECT date_trunc('day', some_column_no_migration_declares) FROM whatever`))
	assertNoTimeBucketFindings(t, root)
}

// The truncated expression is not a plain column reference, so its type
// cannot be read off the schema. `AT TIME ZONE 'UTC'` in particular is
// itself a correct fix — it converts to TIMESTAMP first — and firing on it
// would be exactly backwards.
func TestTimeBucketing_NonColumnExpression_Silent(t *testing.T) {
	for name, expr := range map[string]string{
		"at time zone": "recorded_at AT TIME ZONE 'UTC'",
		"function":     "now()",
		"cast":         "recorded_at::timestamp",
		"placeholder":  "$1",
	} {
		t.Run(name, func(t *testing.T) {
			root := newTelemetryProject(t)
			writeFile(t, root, "internal/handlers/telemetry/queries.go",
				goHandlerWith("SELECT date_trunc('day', "+expr+") FROM telemetry_samples"))
			assertNoTimeBucketFindings(t, root)
		})
	}
}

// A unit finer than an hour cannot cross a real zone boundary — every
// offset in the tz database is a whole number of minutes — so bucketing by
// minute or second is session-independent in practice.
func TestTimeBucketing_SubHourUnit_Silent(t *testing.T) {
	for _, unit := range []string{"minute", "second", "milliseconds"} {
		t.Run(unit, func(t *testing.T) {
			root := newTelemetryProject(t)
			writeFile(t, root, "internal/handlers/telemetry/queries.go",
				goHandlerWith("SELECT date_trunc('"+unit+"', recorded_at) FROM telemetry_samples"))
			assertNoTimeBucketFindings(t, root)
		})
	}
}

// The unit is supplied at runtime, so the check cannot know whether it is
// coarse enough to matter.
func TestTimeBucketing_NonLiteralUnit_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/queries.go",
		goHandlerWith("SELECT date_trunc($1, recorded_at) FROM telemetry_samples"))
	assertNoTimeBucketFindings(t, root)
}

// A column name declared TIMESTAMPTZ on one table and TIMESTAMP on another
// cannot be attributed from a bare reference, and guessing would report a
// correct query half the time.
func TestTimeBucketing_AmbiguousColumnType_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "db/migrations/20240103000000_other.up.sql", `
CREATE TABLE audit_rows (
    id TEXT PRIMARY KEY,
    recorded_at TIMESTAMP NOT NULL
);
`)
	writeFile(t, root, "internal/handlers/telemetry/queries.go",
		goHandlerWith("SELECT date_trunc('day', recorded_at) FROM telemetry_samples"))
	assertNoTimeBucketFindings(t, root)
}

// date_trunc mentioned in a Go comment or in prose is not a query. Only
// text inside a string literal is considered, which makes this class of
// false positive structurally impossible rather than merely filtered.
func TestTimeBucketing_OutsideStringLiteral_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/handlers/telemetry/notes.go",
		"package handlers\n\n// Prefer date_trunc('day', recorded_at) here one day.\nconst unrelated = 1\n")
	assertNoTimeBucketFindings(t, root)
}

// A commented-out query in a migration is not a statement.
func TestTimeBucketing_CommentedMigrationSQL_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "db/migrations/20240204000000_notes.up.sql",
		"-- SELECT date_trunc('day', recorded_at) FROM telemetry_samples;\n")
	assertNoTimeBucketFindings(t, root)
}

// Generated files are forge's, and a project with no migrations has no
// schema to resolve a column against.
func TestTimeBucketing_GeneratedFileAndNoMigrations_Silent(t *testing.T) {
	root := newTelemetryProject(t)
	writeFile(t, root, "internal/telemetry/repo_gen.go",
		goHandlerWith("SELECT date_trunc('day', recorded_at) FROM telemetry_samples"))
	assertNoTimeBucketFindings(t, root)

	bare := t.TempDir()
	writeFile(t, bare, "internal/handlers/telemetry/queries.go",
		goHandlerWith("SELECT date_trunc('day', recorded_at) FROM telemetry_samples"))
	findings, err := collectTimeBucketFindings(bare, "db/migrations")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("got %d findings with no migrations, want 0: %+v", len(findings), findings)
	}
}

// ── Report shape ─────────────────────────────────────────────────────────

func assertNoTimeBucketFindings(t *testing.T, root string) {
	t.Helper()
	findings, err := collectTimeBucketFindings(root, "db/migrations")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(findings), findings)
	}
}
