package codegen

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// The guard's contract, verified against a real postgres.
//
// Every assertion here derives from something a PRODUCER computed — the
// derivation's own output, or postgres's own verdict on it. Nothing greps the
// rendered file for a literal, and nothing hardcodes a value the code under
// test also hardcodes: a fixture is judged by asking the applied schema
// whether it accepts it, which is the same question the generated test's
// create #1 asks.

// guardSchema is the shopdemo schema that broke the real run, reduced to the
// constraints that broke it — the email regex, the `= 2` country code, the
// BETWEEN name — plus the two forms probing showed the derivation cannot
// invert: a numeric IN-list, and a regex postgres accepts but Go's RE2
// rejects.
const guardSchema = `
CREATE TABLE orders (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    customer_id TEXT NOT NULL CHECK (char_length(customer_id) >= 1),
    customer_email TEXT NOT NULL CHECK (customer_email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
    status TEXT NOT NULL CHECK (status IN ('ORDER_STATUS_PENDING', 'ORDER_STATUS_PAID')),
    subtotal_cents BIGINT NOT NULL CHECK (subtotal_cents >= 0),
    shipping_name TEXT NOT NULL CHECK (char_length(shipping_name) BETWEEN 1 AND 160),
    shipping_country TEXT NOT NULL CHECK (char_length(shipping_country) = 2),
    priority BIGINT NOT NULL CHECK (priority IN (10, 20, 30))
);
`

// applyGuardSchema writes migrations to a temp dir and runs the PRODUCTION
// introspection path over them, returning the model plus the live shadow.
func applyGuardSchema(t *testing.T, ddl string) ([]schemadef.Table, *schemadef.Shadow) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(ddl), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	tables, shadow, err := schemadef.ApplyAndIntrospectShadowAt(dir, "")
	if err != nil {
		if shadow != nil {
			shadow.Close()
		}
		t.Fatalf("apply+introspect: %v", err)
	}
	t.Cleanup(shadow.Close)
	if len(tables) == 0 {
		t.Fatal("introspection returned no tables — the guard would verify nothing")
	}
	return tables, shadow
}

func tableNamed(t *testing.T, tables []schemadef.Table, name string) schemadef.Table {
	t.Helper()
	for _, tb := range tables {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("table %q not introspected", name)
	return schemadef.Table{}
}

// TestFixtureGuard_AcceptsWhatPostgresAccepts_RejectsWhatItRejects is the
// guard's central property, and it is stated ENTIRELY in postgres's terms.
//
// For every single-column CHECK in the applied schema, a value is INSERTed
// directly and postgres's accept/reject is recorded; the guard is then asked
// about the same value. The two verdicts must agree on every case. That makes
// the expectation the DATABASE's behaviour rather than a list this test
// maintains — if postgres changes its mind about a value, the expectation
// moves with it, and a guard that disagreed with the INSERT it exists to
// predict would fail here.
func TestFixtureGuard_AcceptsWhatPostgresAccepts_RejectsWhatItRejects(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a live Postgres; runs in task test")
	}
	tables, shadow := applyGuardSchema(t, guardSchema)
	orders := tableNamed(t, tables, "orders")
	ctx := context.Background()

	// Candidate values per column: one the derivation produces, and one
	// deliberately hostile. Both are run past postgres for ground truth.
	candidates := map[string][]string{
		"customer_email":   {`"1@1.1"`, `"sample_customer_email_1"`},
		"shipping_country": {`"US"`, `"sample_shipping_country_1"`},
		"shipping_name":    {`"sample_shipping_name_1"`, `""`},
		"customer_id":      {`"sample_customer_id_1"`, `""`},
		"status":           {`"ORDER_STATUS_PAID"`, `"test-value"`},
	}

	cases := 0
	for column, vals := range candidates {
		for _, goLit := range vals {
			sqlLit, ok := goLiteralToSQL(goLit, "string")
			if !ok {
				t.Fatalf("%s: goLiteralToSQL could not read %s", column, goLit)
			}
			// Ground truth: does postgres itself accept this value in this
			// column? Asked by INSERTing a row that is otherwise valid.
			pgAccepts := insertProbe(ctx, t, shadow, column, sqlLit)

			// The guard's verdict on the same value.
			vs, judged, err := verifyFixtures(ctx, shadow.DB(), orders,
				[]fixtureValue{{column: column, sqlLit: sqlLit, goLit: goLit}})
			if err != nil {
				t.Fatalf("%s=%s: verifyFixtures: %v", column, goLit, err)
			}
			// Agreement is only meaningful if postgres was actually asked.
			if judged == 0 {
				t.Fatalf("%s=%s: guard evaluated no constraint — agreement would be vacuous", column, goLit)
			}
			guardAccepts := len(vs) == 0

			if guardAccepts != pgAccepts {
				t.Errorf("%s = %s: guard says accepted=%v, postgres says accepted=%v",
					column, goLit, guardAccepts, pgAccepts)
			}
			// A rejection must NAME the column and the constraint — that is
			// the whole point of failing at generate time.
			if !guardAccepts {
				v := vs[0]
				if v.Column != column {
					t.Errorf("%s: violation names column %q", column, v.Column)
				}
				if v.Constraint == "" {
					t.Errorf("%s: violation names no constraint", column)
				}
				if !strings.Contains(v.Error(), column) || !strings.Contains(v.Error(), v.Constraint) {
					t.Errorf("%s: message omits column or constraint: %s", column, v.Error())
				}
			}
			cases++
		}
	}
	if cases == 0 {
		t.Fatal("no candidate values exercised — the derived case set was empty")
	}
	t.Logf("guard agreed with postgres on %d value(s)", cases)
}

// insertProbe reports whether postgres accepts value in the given column, by
// INSERTing a row whose OTHER columns are known-good and rolling back. This is
// the ground truth the guard is measured against: it is literally the create
// the generated lifecycle test performs.
func insertProbe(ctx context.Context, t *testing.T, shadow *schemadef.Shadow, column, sqlLit string) bool {
	t.Helper()
	good := map[string]string{
		"id":               `'x'`,
		"customer_id":      `'c'`,
		"customer_email":   `'a@b.c'`,
		"status":           `'ORDER_STATUS_PAID'`,
		"subtotal_cents":   `0`,
		"shipping_name":    `'n'`,
		"shipping_country": `'US'`,
		"priority":         `10`,
	}
	if _, ok := good[column]; !ok {
		t.Fatalf("insertProbe has no baseline row value for column %q", column)
	}
	good[column] = sqlLit
	cols := make([]string, 0, len(good))
	vals := make([]string, 0, len(good))
	for c, v := range good {
		cols = append(cols, c)
		vals = append(vals, v)
	}
	tx, err := shadow.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "INSERT INTO orders ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(vals, ",")+")")
	return err == nil
}

// TestFixtureGuard_CatchesRE2IncompatibleRegex pins the silent hole the guard
// was built for: a CHECK whose regex postgres accepts and Go's RE2 cannot
// compile. The derivation cannot invert it, falls back to the placeholder, and
// before the guard NOTHING said so.
//
// The test asserts the two halves of the defect separately, so a future change
// that makes the derivation smarter does not make this test vacuous: first
// that the derivation really does fall back here (checked by asking seeddata,
// the producer), then that the guard rejects what it fell back to.
func TestFixtureGuard_CatchesRE2IncompatibleRegex(t *testing.T) {
	const ddl = `
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    passcode TEXT NOT NULL CHECK (passcode ~ '^(?=.*[A-Z]).{8,}$')
);`
	tables, shadow := applyGuardSchema(t, ddl)
	accounts := tableNamed(t, tables, "accounts")
	col, ok := tableColumn(accounts, "passcode")
	if !ok {
		t.Fatal("passcode column not introspected")
	}

	// Half one: the producer falls back. SynthString's own contract is that a
	// value it derived from a pattern does NOT carry the synthetic prefix, so
	// the prefix is the producer's own signal that it invented a placeholder.
	got := seedplan.SynthString(accounts, col, 0)
	if !strings.HasPrefix(got, seedplan.SyntheticStringPrefix) {
		t.Skipf("derivation now inverts this pattern (%q) — the fallback this guards is gone", got)
	}

	// Half two: the guard rejects the fallback, because postgres does.
	sqlLit, ok := goLiteralToSQL(`"`+got+`"`, "string")
	if !ok {
		t.Fatalf("goLiteralToSQL could not read %q", got)
	}
	vs, judged, err := verifyFixtures(context.Background(), shadow.DB(), accounts,
		[]fixtureValue{{column: "passcode", sqlLit: sqlLit, goLit: `"` + got + `"`}})
	if err != nil {
		t.Fatalf("verifyFixtures: %v", err)
	}
	if judged == 0 {
		t.Fatal("guard evaluated no constraint — it never saw the regex CHECK")
	}
	if len(vs) != 1 {
		t.Fatalf("guard reported %d violation(s) for a value postgres rejects, want 1", len(vs))
	}
	if !strings.Contains(vs[0].Error(), "passcode") {
		t.Errorf("message does not name the column: %s", vs[0].Error())
	}
	t.Logf("guard caught the silent fallback: %s", vs[0].Error())
}

// TestFixtureGuard_SuggestedTypeActuallyFixesIt closes the loop on the error
// message. A remedy that names a type is only useful if that type's values
// PASS the constraint that just failed — so the test takes the guard's own
// suggestion, generates values from it, and requires postgres to accept them.
//
// Without this, the message could confidently suggest a type whose values the
// schema also rejects, and the author would follow the instructions into a
// second failure.
func TestFixtureGuard_SuggestedTypeActuallyFixesIt(t *testing.T) {
	// A country column constrained by a LOOKAHEAD — a construct postgres's
	// POSIX engine accepts and Go's RE2 cannot compile, so the derivation has
	// nothing to invert and falls back to a placeholder the schema rejects.
	// Inference (segment `country` + an exactly-2 length bound) then forces
	// the country_code reading, which is what the guard suggests.
	const ddl = `
CREATE TABLE shipments (
    id TEXT PRIMARY KEY,
    shipping_country TEXT NOT NULL
        CHECK (char_length(shipping_country) = 2
               AND shipping_country ~ '^(?=[A-Z])[A-Z]{2}$')
);`
	tables, shadow := applyGuardSchema(t, ddl)
	shipments := tableNamed(t, tables, "shipments")
	ctx := context.Background()

	// The derived fixture, via the production path.
	fx := &crudTestFixtures{
		tables:  map[string]schemadef.Table{"shipments": shipments},
		pools:   seedplan.PoolsFromTables(tables),
		bounds:  seedplan.BoundsFromTables(tables),
		plans:   map[string]*entitySeedPlan{},
		emitted: map[string][]fixtureValue{},
	}
	col, _ := tableColumn(shipments, "shipping_country")
	v1, _, ok := fx.stringFixture(shipments, col)
	if !ok {
		t.Fatal("no fixture derived")
	}
	sqlLit, _ := goLiteralToSQL(v1, "string")
	vs, judged, err := verifyFixtures(ctx, shadow.DB(), shipments,
		[]fixtureValue{{column: "shipping_country", sqlLit: sqlLit, goLit: v1}})
	if err != nil {
		t.Fatalf("verifyFixtures: %v", err)
	}
	if judged == 0 {
		t.Fatal("guard evaluated nothing")
	}
	if len(vs) == 0 {
		t.Skipf("the derivation now satisfies this constraint (%s) — no suggestion needed", v1)
	}

	suggested := vs[0].SuggestedType
	if suggested == "" {
		t.Fatalf("violation offers no type suggestion for a column whose constraints force one: %s", vs[0].Error())
	}
	if !strings.Contains(vs[0].Error(), suggested) {
		t.Errorf("message does not carry the suggestion: %s", vs[0].Error())
	}

	// The suggestion must WORK: every value it generates passes the guard.
	vals, err := seedplan.VocabTypeValues(suggested, 0, "shipments", "shipping_country", 8)
	if err != nil {
		t.Fatalf("suggested type %q: %v", suggested, err)
	}
	if len(vals) == 0 {
		t.Fatalf("suggested type %q generated nothing", suggested)
	}
	for _, v := range vals {
		lit, okL := goLiteralToSQL(strconv.Quote(v), "string")
		if !okL {
			t.Fatalf("cannot render %q", v)
		}
		bad, _, err := verifyFixtures(ctx, shadow.DB(), shipments,
			[]fixtureValue{{column: "shipping_country", sqlLit: lit, goLit: v}})
		if err != nil {
			t.Fatalf("verify suggested value: %v", err)
		}
		if len(bad) != 0 {
			t.Errorf("suggested type %q produced %q, which the constraint still rejects", suggested, v)
		}
	}
	t.Logf("guard suggested {type: %s}; all %d generated values pass the constraint that failed", suggested, len(vals))
}

// TestFixtureGuard_NumericInListFixtureIsAMember covers the second silent
// hole: a numeric IN-list. The fixture must be a MEMBER of the vocabulary —
// no clamp can satisfy a set — and the assertion derives the admissible set
// from the parsed pool, then requires postgres to agree.
func TestFixtureGuard_NumericInListFixtureIsAMember(t *testing.T) {
	tables, shadow := applyGuardSchema(t, guardSchema)
	orders := tableNamed(t, tables, "orders")

	fx := &crudTestFixtures{
		tables:  map[string]schemadef.Table{"orders": orders},
		pools:   seedplan.PoolsFromTables(tables),
		bounds:  seedplan.BoundsFromTables(tables),
		plans:   map[string]*entitySeedPlan{},
		emitted: map[string][]fixtureValue{},
	}

	// The producer's own recovered vocabulary is the expectation.
	members := fx.pools["orders"]["priority"]
	if len(members) == 0 {
		t.Fatal("no numeric vocabulary recovered for orders.priority — nothing to assert against")
	}
	admissible := map[string]bool{}
	for _, m := range members {
		admissible[m] = true
	}

	v1, v2, ok := fx.intFixture("orders", "priority")
	if !ok {
		t.Fatal("intFixture produced no value for a column carrying an IN-list CHECK")
	}
	for _, v := range []string{v1, v2} {
		if !admissible[v] {
			t.Errorf("fixture %s is not a member of the recovered vocabulary %v", v, members)
		}
	}

	// And postgres agrees.
	vs, judged, err := verifyFixtures(context.Background(), shadow.DB(), orders,
		[]fixtureValue{{column: "priority", sqlLit: v1, goLit: v1}})
	if err != nil {
		t.Fatalf("verifyFixtures: %v", err)
	}
	if judged == 0 {
		t.Fatal("guard evaluated no constraint for priority")
	}
	if len(vs) != 0 {
		t.Errorf("postgres rejects the derived fixture: %v", vs)
	}
	t.Logf("orders.priority fixtures %s/%s drawn from vocabulary %v", v1, v2, members)
}

// TestFixtureGuard_MultiColumnChecksAreNotJudged pins a deliberate silence.
// A CHECK spanning two columns cannot be evaluated from a per-column fixture
// set — the other side may not be in the create request at all — and reporting
// a violation there would be inventing one.
//
// The assertion is on `judged`, not merely on "no violations". Those are
// different claims, and the difference is exactly what a first draft of this
// test got wrong: it asserted only that nothing was reported, which stayed
// green even with the multi-column skip removed (the query then fails on the
// unbound column and the guard stays silent for the WRONG reason). Requiring
// that the single-column CHECK in the same table IS judged, while the
// two-column one contributes nothing, separates the two.
func TestFixtureGuard_MultiColumnChecksAreNotJudged(t *testing.T) {
	const ddl = `
CREATE TABLE windows (
    id TEXT PRIMARY KEY,
    opens_at BIGINT NOT NULL,
    closes_at BIGINT NOT NULL CHECK (closes_at >= 0),
    CONSTRAINT windows_order_check CHECK (closes_at > opens_at)
);`
	tables, shadow := applyGuardSchema(t, ddl)
	windows := tableNamed(t, tables, "windows")

	// Confirm the schema really does carry both shapes, or the test proves
	// nothing about the branch it claims to cover.
	var single, multi int
	for _, ck := range windows.Checks {
		if len(ck.Columns) > 1 {
			multi++
		} else if len(ck.Columns) == 1 && ck.Columns[0] == "closes_at" {
			single++
		}
	}
	if multi == 0 || single == 0 {
		t.Fatalf("need both check shapes on closes_at, got multi=%d single=%d", multi, single)
	}

	// closes_at = 1 SATISFIES its single-column CHECK (>= 0) and VIOLATES the
	// two-column one (1 > opens_at is false for opens_at >= 1). The guard must
	// judge exactly the first: one verdict, no violations.
	vs, judged, err := verifyFixtures(context.Background(), shadow.DB(), windows,
		[]fixtureValue{{column: "closes_at", sqlLit: "1", goLit: "1"}})
	if err != nil {
		t.Fatalf("verifyFixtures: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("guard judged a multi-column CHECK from one column: %v", vs)
	}
	if judged != single {
		t.Errorf("guard returned %d verdict(s), want %d (the single-column CHECKs only)", judged, single)
	}
}
