package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// vocab.yaml used to accept only values that render as a quoted string or a
// bare number, so the three scalar kinds every app has could not be described:
//
//   - `[null, "x"]` silently DROPPED the null (yaml decodes a null into a
//     []string as nothing), and `[null]` failed with "has no values";
//   - a BOOLEAN column was refused ("does not take a vocabulary"), so its
//     ratio was always the hash's 50/50;
//   - a TIMESTAMPTZ/DATE column was refused too, so every seeded date sat in
//     January 2024 however far "now" had moved on.
//
// These tests pin the vocabulary for all three.

// scalarSchema is one table with a column of each scalar kind the vocabulary
// now describes, nullable and NOT NULL.
func scalarSchema() []schemadef.Table {
	return []schemadef.Table{{
		Name:   "jobs",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("notes", schemadef.TypeString, false, false),
			col("title", schemadef.TypeString, true, false),
			col("insured", schemadef.TypeBool, true, false),
			col("scheduled_for", schemadef.TypeTime, false, false),
			col("starts_at", schemadef.TypeTime, true, false),
			{Name: "due_on", Type: schemadef.TypeTime, NotNull: true, DeclType: "DATE"},
		},
	}}
}

// scalarPlan loads a vocab FILE (the user-facing grammar, not a hand-built
// Vocab) and applies it to a fresh plan of the scalar schema.
func scalarPlan(t *testing.T, rows int, vocabYAML string) (*Plan, []string) {
	t.Helper()
	v, err := LoadVocab(writeVocab(t, vocabYAML))
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	p := buildOrFail(t, scalarSchema(), Config{Rows: rows, Salt: 4})
	return p, p.ApplyVocab(v)
}

func countCells(cells []string) map[string]int {
	out := map[string]int{}
	for _, c := range cells {
		out[c]++
	}
	return out
}

// A null in a list is a real value for a NULLABLE column, weighted like any
// other entry — and `[null]` alone pins the column to NULL.
func TestVocab_NullSeedsANullableColumn(t *testing.T) {
	p, warns := scalarPlan(t, 60, "columns:\n  jobs.notes: [null, \"Gate code 4411\"]\n")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	got := countCells(cellsFor(p, "jobs", "notes"))
	if got["NULL"] == 0 {
		t.Errorf("jobs.notes: [null, x] seeded no NULL in 60 rows (the null was dropped): %v", got)
	}
	if got["'Gate code 4411'"] == 0 {
		t.Errorf("jobs.notes: [null, x] seeded no 'Gate code 4411' in 60 rows: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("jobs.notes drew values outside its vocabulary: %v", got)
	}

	pinned, warns := scalarPlan(t, 10, "columns:\n  jobs.notes: [null]\n")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	for i, c := range cellsFor(pinned, "jobs", "notes") {
		if c != "NULL" {
			t.Fatalf("jobs.notes: [null] row %d = %s, want NULL", i, c)
		}
	}
	if err := pinned.Validate(); err != nil {
		t.Errorf("a null on a NULLABLE column must not refuse the seed: %v", err)
	}
}

// A null on a NOT NULL column cannot be inserted, and it is an authoring
// mistake rather than a value to skip quietly: the plan refuses to apply, and
// the refusal names the column.
func TestVocab_NullOnANotNullColumnRefusesTheSeed(t *testing.T) {
	p, _ := scalarPlan(t, 10, "columns:\n  jobs.title: [null, \"Re-roof\"]\n")
	err := p.Validate()
	if err == nil {
		t.Fatal("jobs.title is NOT NULL, yet a vocab null on it did not refuse the seed")
	}
	for _, want := range []string{"jobs.title", "NOT NULL", "null"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must mention %q; got:\n%s", want, err)
		}
	}
	// Codegen consumers (mocks, factories) never call Validate; for them the
	// null is skipped so the column still draws a valid value.
	for i, c := range cellsFor(p, "jobs", "title") {
		if c != "'Re-roof'" {
			t.Fatalf("jobs.title row %d = %s, want the non-null survivor 'Re-roof'", i, c)
		}
	}
}

// A boolean column takes a list of true/false; repeating an entry weights the
// draw, exactly as it does for any other list.
func TestVocab_BooleansAreWeightedByRepetition(t *testing.T) {
	pinned, warns := scalarPlan(t, 30, "columns:\n  jobs.insured: [true]\n")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	for i, c := range cellsFor(pinned, "jobs", "insured") {
		if c != "true" {
			t.Fatalf("jobs.insured: [true] row %d = %s, want true", i, c)
		}
	}

	weighted, warns := scalarPlan(t, 400, "columns:\n  jobs.insured: [true, true, true, false]\n")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	got := countCells(cellsFor(weighted, "jobs", "insured"))
	if len(got) != 2 {
		t.Fatalf("jobs.insured drew %v; want only bare true/false literals", got)
	}
	if share := float64(got["true"]) / 400; share < 0.65 || share > 0.85 {
		t.Errorf("[true, true, true, false] seeded true on %.0f%% of rows; want ~75%%", share*100)
	}

	// Anything that is not a boolean is skipped with a warning, like every
	// other invalid vocab value.
	_, warns = scalarPlan(t, 5, "columns:\n  jobs.insured: [true, maybe]\n")
	if !containsSub(warns, `jobs.insured: value "maybe"`) {
		t.Errorf("a non-boolean value must be skipped with a warning; got %v", warns)
	}
}

// Timestamps are written RELATIVE TO NOW — `{from, to}` for a range, and
// `now` / `-3d` / `+1w` tokens in a list — so seeded dates sit around the day
// the seed runs instead of a fixed year.
func TestVocab_TimestampsRelativeToNow(t *testing.T) {
	p, warns := scalarPlan(t, 200, `
columns:
  jobs.starts_at: {from: -90d, to: +30d}
  jobs.scheduled_for: [null, -3d, now, +1w]
  jobs.due_on: {from: -2w, to: +2w, step: 1d}
`)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	// The range: every value parses, the spread covers most of the declared
	// 120 days, and never exceeds it.
	starts := parseTimes(t, cellsFor(p, "jobs", "starts_at"))
	lo, hi := minMax(starts)
	if span := hi.Sub(lo); span > 120*24*time.Hour || span < 90*24*time.Hour {
		t.Errorf("{from: -90d, to: +30d} spread over %s; want close to (and at most) 120 days", span)
	}

	// The list: exactly the three instants (plus NULL), 3 days and 7 days
	// either side of the same "now".
	var listed []string
	nulls := 0
	for _, c := range cellsFor(p, "jobs", "scheduled_for") {
		if c == "NULL" {
			nulls++
			continue
		}
		listed = append(listed, c)
	}
	if nulls == 0 {
		t.Error("jobs.scheduled_for: [null, ...] seeded no NULL in 200 rows")
	}
	distinct := map[time.Time]bool{}
	for _, at := range parseTimes(t, listed) {
		distinct[at] = true
	}
	if len(distinct) != 3 {
		t.Fatalf("jobs.scheduled_for: [-3d, now, +1w] produced %d distinct instants, want 3: %v", len(distinct), distinct)
	}
	var instants []time.Time
	for at := range distinct {
		instants = append(instants, at)
	}
	first, last := minMax(instants)
	if got := last.Sub(first); got != 10*24*time.Hour {
		t.Errorf("-3d .. +1w spans %s, want exactly 240h", got)
	}

	// A DATE column with a day step: whole days only, within the 4 weeks.
	dues := parseTimes(t, cellsFor(p, "jobs", "due_on"))
	dlo, dhi := minMax(dues)
	if span := dhi.Sub(dlo); span > 28*24*time.Hour {
		t.Errorf("due_on spread %s exceeds the declared 28 days", span)
	}
	for _, d := range dues {
		if d.Sub(dlo)%(24*time.Hour) != 0 {
			t.Fatalf("due_on value %s is not a whole number of days from %s; step: 1d must hold", d, dlo)
		}
	}
}

// Malformed time entries are load-time errors naming the line, like every
// other malformed vocab entry.
func TestLoadVocab_TimeRangeRejectsMalformed(t *testing.T) {
	cases := []struct{ name, content, wantErr string }{
		{"missing to", "columns:\n  jobs.starts_at: {from: -3d}\n", "needs both from and to"},
		{"bad unit", "columns:\n  jobs.starts_at: {from: -3x, to: now}\n", `"-3x"`},
		{"to before from", "columns:\n  jobs.starts_at: {from: +3d, to: -3d}\n", "before"},
		{"mixed with min", "columns:\n  jobs.starts_at: {from: -3d, to: now, min: 1}\n", "not several"},
	}
	for _, c := range cases {
		_, err := LoadVocab(writeVocab(t, c.content))
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want mention of %s", c.name, err, c.wantErr)
		}
	}
}

// The same overlay applied on a real postgres: every literal the new value
// kinds render is accepted by the INSERT (a single bad one would abort the
// transactional seed), and the stored values are the declared ones.
func TestMaterialize_ScalarVocabOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	const migration = `
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    notes TEXT,
    insured BOOLEAN NOT NULL DEFAULT false,
    scheduled_for TIMESTAMPTZ,
    due_on DATE NOT NULL DEFAULT CURRENT_DATE
);`
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(migration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	base := t.TempDir()
	migDir := filepath.Join(base, "migrations")
	if err := os.MkdirAll(filepath.Join(base, "seeds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(migDir), []byte(`
columns:
  jobs.notes: [null, "Side gate is unlocked"]
  jobs.insured: [true]
  jobs.scheduled_for: {from: -30d, to: +30d}
  jobs.due_on: [-1w, now, +1w]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildLivePlan(ctx, db, migDir, "", Config{Rows: 30, Salt: 1})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	if w := plan.VocabWarnings(); len(w) != 0 {
		t.Fatalf("unexpected vocab warnings: %v", w)
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var nullNotes, valueNotes, notInsured, dueDays int
	var scheduledSpreadDays float64
	if err := db.QueryRowContext(ctx, `
SELECT count(*) FILTER (WHERE notes IS NULL),
       count(*) FILTER (WHERE notes = 'Side gate is unlocked'),
       count(*) FILTER (WHERE NOT insured),
       count(DISTINCT due_on),
       EXTRACT(EPOCH FROM max(scheduled_for) - min(scheduled_for)) / 86400
FROM jobs`).Scan(&nullNotes, &valueNotes, &notInsured, &dueDays, &scheduledSpreadDays); err != nil {
		t.Fatal(err)
	}
	if nullNotes == 0 || valueNotes == 0 || nullNotes+valueNotes != 30 {
		t.Errorf("notes: %d NULL + %d 'Side gate is unlocked' of 30 rows; want both kinds and nothing else", nullNotes, valueNotes)
	}
	if notInsured != 0 {
		t.Errorf("insured: [true] left %d row(s) false", notInsured)
	}
	if dueDays != 3 {
		t.Errorf("due_on: [-1w, now, +1w] stored %d distinct dates, want 3", dueDays)
	}
	if scheduledSpreadDays < 30 || scheduledSpreadDays > 60 {
		t.Errorf("scheduled_for {from: -30d, to: +30d} spread %.1f days, want between 30 and 60", scheduledSpreadDays)
	}
}

// Config.Now anchors BOTH the vocab's relative instants and built-in synthesis
// of undescribed timestamp columns; a zero Now is the fixed reference instant,
// so generate-time consumers stay byte-stable.
func TestConfigNow_AnchorsRelativeAndSynthesizedTimes(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	v, err := LoadVocab(writeVocab(t, "columns:\n  jobs.starts_at: {from: -90d, to: +30d}\n  jobs.scheduled_for: [now]\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := buildOrFail(t, scalarSchema(), Config{Rows: 50, Now: now})
	if warns := p.ApplyVocab(v); len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	for _, at := range parseTimes(t, cellsFor(p, "jobs", "starts_at")) {
		if at.Before(now.AddDate(0, 0, -90)) || at.After(now.AddDate(0, 0, 30)) {
			t.Fatalf("starts_at %s outside [now-90d, now+30d] for now=%s", at, now)
		}
	}
	for _, at := range parseTimes(t, cellsFor(p, "jobs", "scheduled_for")) {
		if !at.Equal(now) {
			t.Fatalf("scheduled_for: [now] = %s, want %s", at, now)
		}
	}

	// Undescribed managed timestamps sit in the four weeks before now.
	anchored := buildOrFail(t, fkSchema(), Config{Rows: 40, Now: now})
	for _, column := range []string{"created_at", "updated_at"} {
		for _, at := range parseTimes(t, cellsFor(anchored, "patients", column)) {
			if !at.Before(now) || at.Before(now.AddDate(0, 0, -28)) {
				t.Fatalf("patients.%s = %s, want within the 28 days before %s", column, at, now)
			}
		}
	}

	// Zero Now: the historical window, exactly — 2024-01-01..28 at 08:00.
	fixed := buildOrFail(t, fkSchema(), Config{Rows: 3})
	if got := cellsFor(fixed, "patients", "created_at"); !equal(got, []string{
		"'2024-01-01T08:00:00Z'", "'2024-01-02T08:00:00Z'", "'2024-01-03T08:00:00Z'",
	}) {
		t.Errorf("zero Config.Now must keep the reference window byte-identical; got %v", got)
	}
}

// A refused plan must refuse BEFORE `seed reset` wipes anything: Reset used to
// TRUNCATE and only then let Apply validate, so a vocab null on a NOT NULL
// column emptied the database and then reported an error.
func TestReset_RefusesBeforeTruncating(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(`CREATE TABLE jobs (id TEXT PRIMARY KEY, title TEXT NOT NULL);
INSERT INTO jobs (id, title) VALUES ('keep-me', 'Existing row');`); err != nil {
		t.Fatal(err)
	}
	tables := []schemadef.Table{{
		Name:    "jobs",
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{col("id", schemadef.TypeString, true, true), col("title", schemadef.TypeString, true, false)},
	}}
	plan := buildOrFail(t, tables, Config{Rows: 3})
	v, err := LoadVocab(writeVocab(t, "columns:\n  jobs.title: [null, \"Re-roof\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	plan.ApplyVocab(v)

	if _, err := Reset(ctx, db, plan); err == nil {
		t.Fatal("Reset applied a plan Validate refuses")
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM jobs WHERE id = 'keep-me'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("a refused reset still truncated the table (keep-me rows = %d)", n)
	}
}

func parseTimes(t *testing.T, cells []string) []time.Time {
	t.Helper()
	out := make([]time.Time, 0, len(cells))
	for _, c := range cells {
		raw, ok := decodeScalarLiteral(c)
		if !ok {
			t.Fatalf("cell %s is not a scalar time literal", c)
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t.Fatalf("cell %s is not RFC3339: %v", c, err)
		}
		out = append(out, at)
	}
	return out
}

func minMax(ts []time.Time) (lo, hi time.Time) {
	for i, at := range ts {
		if i == 0 || at.Before(lo) {
			lo = at
		}
		if i == 0 || at.After(hi) {
			hi = at
		}
	}
	return lo, hi
}

func containsSub(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
