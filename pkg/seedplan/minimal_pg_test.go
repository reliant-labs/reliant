// File: pkg/seedplan/minimal_pg_test.go
//
// Config.Minimal — the smallest row the schema accepts, for test factories.
//
// The defect this pins, as measured in a dogfood run: the generated NewJob
// factory, built from the FULL plan, inserted a LEAD job that already carried
// a crew, a schedule, a completion stamp and a lost reason. Legal against
// every constraint, and a row no lifecycle reaches, so every test that used it
// overrode those fields back off before it could assert anything.

package seedplan

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// minimalJobsDDL is the measured jobs schema, trimmed to the shapes that
// matter: three readable status guards, one guard forge cannot read (its
// consequent is a function call), an ordering CHECK over nullable columns, an
// optional reference, and defaulted columns of every kind.
const minimalJobsDDL = `
CREATE TABLE crews (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    name TEXT NOT NULL CHECK (char_length(name) >= 1),
    headcount BIGINT NOT NULL DEFAULT 0 CHECK (headcount >= 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())
);
CREATE TABLE customers (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    name TEXT NOT NULL CHECK (char_length(name) >= 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())
);
CREATE TABLE jobs (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    customer_id TEXT NOT NULL REFERENCES customers (id),
    title TEXT NOT NULL CHECK (char_length(title) >= 1),
    kind TEXT NOT NULL DEFAULT 'JOB_KIND_REPLACEMENT' CHECK (kind IN ('JOB_KIND_REPLACEMENT', 'JOB_KIND_REPAIR')),
    status TEXT NOT NULL DEFAULT 'JOB_STATUS_LEAD' CHECK (status IN ('JOB_STATUS_LEAD', 'JOB_STATUS_SCHEDULED', 'JOB_STATUS_COMPLETED', 'JOB_STATUS_LOST')),
    crew_id TEXT REFERENCES crews (id),
    scheduled_start TIMESTAMPTZ,
    scheduled_end TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    lost_reason TEXT NOT NULL DEFAULT '',
    insurance_claim BOOLEAN NOT NULL DEFAULT FALSE,
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    CONSTRAINT jobs_schedule_ordered
        CHECK (scheduled_start IS NULL OR scheduled_end IS NULL OR scheduled_end >= scheduled_start),
    CONSTRAINT jobs_scheduled_has_crew_and_dates
        CHECK (status <> 'JOB_STATUS_SCHEDULED' OR (crew_id IS NOT NULL AND scheduled_start IS NOT NULL AND scheduled_end IS NOT NULL)),
    CONSTRAINT jobs_completed_has_stamp
        CHECK (status <> 'JOB_STATUS_COMPLETED' OR completed_at IS NOT NULL),
    CONSTRAINT jobs_lost_has_reason
        CHECK (status <> 'JOB_STATUS_LOST' OR char_length(lost_reason) >= 1)
);
`

// minimalHarness boots postgres, applies ddl, and returns the db.
func minimalHarness(t *testing.T, ddl string) *sql.DB {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.ExecContext(context.Background(), ddl); err != nil {
		t.Fatalf("apply ddl: %v", err)
	}
	return db
}

// buildMinimal plans the schema in minimal mode, the way the factory
// generator does.
func buildMinimal(t *testing.T, ddl string, rows int) *Plan {
	t.Helper()
	tables := applyDDL(t, ddl)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: rows, Minimal: true})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	return p
}

// TestMinimalPlan_JobIsAFreshLead is the reproduction: the minimal job is at
// its DEFAULT status with every lifecycle column NULL or defaulted, and it
// inserts.
func TestMinimalPlan_JobIsAFreshLead(t *testing.T) {
	p := buildMinimal(t, minimalJobsDDL, 2)
	db := minimalHarness(t, minimalJobsDDL)
	ctx := context.Background()
	for _, stmt := range p.Statements() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("minimal row rejected by its own schema: %v\n%s", err, stmt)
		}
	}

	var (
		status, lostReason, kind string
		crewID                   sql.NullString
		start, end, completed    sql.NullTime
		insurance                bool
	)
	if err := db.QueryRowContext(ctx, `SELECT status, kind, crew_id, scheduled_start, scheduled_end,
	        completed_at, lost_reason, insurance_claim FROM jobs ORDER BY id LIMIT 1`).
		Scan(&status, &kind, &crewID, &start, &end, &completed, &lostReason, &insurance); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if status != "JOB_STATUS_LEAD" {
		t.Errorf("status = %q, want the column DEFAULT JOB_STATUS_LEAD (a fresh row's lifecycle state)", status)
	}
	if kind != "JOB_KIND_REPLACEMENT" {
		t.Errorf("kind = %q, want its DEFAULT", kind)
	}
	if crewID.Valid || start.Valid || end.Valid || completed.Valid {
		t.Errorf("a fresh lead carries lifecycle data: crew=%v start=%v end=%v completed=%v",
			crewID, start, end, completed)
	}
	if lostReason != "" || insurance {
		t.Errorf("defaulted columns were written: lost_reason=%q insurance_claim=%t", lostReason, insurance)
	}

	// The INSERT names only what the schema requires.
	for _, col := range []string{"status", "kind", "crew_id", "scheduled_start", "completed_at", "lost_reason", "notes", "created_at"} {
		if p.Writes("jobs", col) {
			t.Errorf("minimal plan writes jobs.%s, which the database supplies", col)
		}
	}
	for _, col := range []string{"id", "customer_id", "title"} {
		if !p.Writes("jobs", col) {
			t.Errorf("minimal plan omits jobs.%s, which the schema requires", col)
		}
	}
	// The optional parent is not needed by a minimal job, so it is not
	// written — but the full plan's crews table is still planned.
	if p.Writes("jobs", "crew_id") {
		t.Error("an optional reference is written on a minimal row")
	}
}

// TestMinimalPlan_FullPlanUnchanged is the negative control: the same schema
// in full mode still populates the lifecycle columns, so the dev dataset keeps
// its coverage.
func TestMinimalPlan_FullPlanUnchanged(t *testing.T) {
	tables := applyDDL(t, minimalJobsDDL)
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 4})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.SetBounds(BoundsFromTables(tables))
	for _, col := range []string{"status", "lost_reason", "notes", "created_at"} {
		if !p.Writes("jobs", col) {
			t.Errorf("full plan omits jobs.%s", col)
		}
	}
	stmts := strings.Join(p.Statements(), "\n")
	if !strings.Contains(stmts, `"lost_reason"`) {
		t.Errorf("full plan no longer names lost_reason:\n%s", stmts)
	}
}

// A DEFAULT whose initial state the schema guards: the minimal branch is the
// default one, and its consequent is written.
const minimalGuardedDefaultDDL = `
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('PENDING', 'ACTIVE')),
    activated_at TIMESTAMPTZ,
    CONSTRAINT accounts_active_has_stamp CHECK (status <> 'ACTIVE' OR activated_at IS NOT NULL)
);
`

func TestMinimalPlan_GuardedDefaultWritesItsConsequent(t *testing.T) {
	p := buildMinimal(t, minimalGuardedDefaultDDL, 1)
	db := minimalHarness(t, minimalGuardedDefaultDDL)
	for _, stmt := range p.Statements() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("minimal row rejected: %v\n%s", err, stmt)
		}
	}
	// PENDING needs no stamp but moves the status off its DEFAULT; ACTIVE
	// keeps the DEFAULT and needs the stamp. The DEFAULT is a fresh row's
	// state — and what a create leaving the status unset stores — so it wins.
	if p.Writes("accounts", "status") {
		t.Error("the minimal row pins status away from its DEFAULT")
	}
	if !p.Writes("accounts", "activated_at") {
		t.Error("the DEFAULT status requires activated_at, and the minimal row leaves it NULL")
	}
}

// A DEFAULT the column's own CHECK rejects: leaving the column out cannot
// succeed, so the minimal row writes it.
const minimalBadDefaultDDL = `
CREATE TABLE codes (
    id TEXT PRIMARY KEY,
    code TEXT NOT NULL DEFAULT '' CHECK (char_length(code) >= 3),
    level BIGINT NOT NULL DEFAULT 0 CHECK (level >= 1),
    label TEXT NOT NULL DEFAULT 'x' CHECK (label IN ('alpha', 'beta')),
    note TEXT NOT NULL DEFAULT ''
);
`

func TestMinimalPlan_DefaultViolatingOwnCheckIsWritten(t *testing.T) {
	p := buildMinimal(t, minimalBadDefaultDDL, 2)
	db := minimalHarness(t, minimalBadDefaultDDL)
	for _, stmt := range p.Statements() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("minimal row rejected: %v\n%s", err, stmt)
		}
	}
	for _, col := range []string{"code", "level", "label"} {
		if !p.Writes("codes", col) {
			t.Errorf("codes.%s has a DEFAULT its own CHECK rejects, yet the minimal row leaves it out", col)
		}
	}
	if p.Writes("codes", "note") {
		t.Error("codes.note's DEFAULT is fine, yet the minimal row writes it")
	}
}

// GENERATED columns are never written, minimal or not; a UNIQUE NOT NULL
// column with a DEFAULT is written so two rows do not collide on it.
const minimalGeneratedDDL = `
CREATE TABLE invoices (
    id TEXT PRIMARY KEY,
    number TEXT NOT NULL DEFAULT 'draft' UNIQUE,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    paid_cents BIGINT NOT NULL DEFAULT 0 CHECK (paid_cents >= 0),
    balance_cents BIGINT GENERATED ALWAYS AS (amount_cents - paid_cents) STORED
);
`

func TestMinimalPlan_GeneratedSkippedUniqueWritten(t *testing.T) {
	p := buildMinimal(t, minimalGeneratedDDL, 2)
	db := minimalHarness(t, minimalGeneratedDDL)
	for _, stmt := range p.Statements() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("minimal rows rejected: %v\n%s", err, stmt)
		}
	}
	if p.Writes("invoices", "balance_cents") {
		t.Error("a GENERATED column is written")
	}
	if !p.Writes("invoices", "number") {
		t.Error("a UNIQUE column left to its DEFAULT collides on the second row")
	}
	if p.Writes("invoices", "paid_cents") {
		t.Error("paid_cents' DEFAULT satisfies its CHECK, yet it is written")
	}
	if _, ok := p.SeedValue("invoices", "paid_cents", 0); ok {
		t.Error("SeedValue reports a value for a column the plan does not write")
	}
	if _, ok := p.NaturalValue("invoices", "paid_cents", 0); !ok {
		t.Error("NaturalValue has no value for a planned, omitted column")
	}
}

func TestLiteralDefault(t *testing.T) {
	cases := []struct {
		def  string
		want string
		ok   bool
	}{
		{`'JOB_STATUS_LEAD'::text`, "JOB_STATUS_LEAD", true},
		{`''::text`, "", true},
		{`'it''s'::character varying`, "it's", true},
		{`0`, "0", true},
		{`'-1'::integer`, "-1", true},
		{`(-1)`, "-1", true},
		{`false`, "false", true},
		{`'{}'::jsonb`, "{}", true},
		{`now()`, "", false},
		{`(now())`, "", false},
		{`gen_random_uuid()`, "", false},
		{``, "", false},
	}
	for _, tc := range cases {
		got, ok := literalDefault(tc.def)
		if got != tc.want || ok != tc.ok {
			t.Errorf("literalDefault(%q) = (%q, %t), want (%q, %t)", tc.def, got, ok, tc.want, tc.ok)
		}
	}
}
