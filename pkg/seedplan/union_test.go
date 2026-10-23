// File: pkg/seedplan/union_test.go
//
// The discriminated union is the second multi-column CHECK shape this package
// places (ordering.go is the first), and these tests pin the same two halves:
// the shape forge SATISFIES — so the rows hold by construction, not by chance
// — and the far larger set of shapes it refuses to guess at, which must keep
// reaching the warning rather than being silently assumed satisfied.

package seedplan

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// couponsPayloadCheck is the constraint verbatim as pg_get_constraintdef
// renders it — produced by applying the real migration to real postgres and
// reading the catalog back, not by guessing how postgres canonicalizes.
const couponsPayloadCheck = `CHECK ((((kind = 'wallet_credit'::text) AND (amount_cents IS NOT NULL) ` +
	`AND (amount_cents > 0) AND (compute_minutes IS NULL)) OR ((kind = 'compute_minutes'::text) ` +
	`AND (compute_minutes IS NOT NULL) AND (compute_minutes > 0) AND (amount_cents IS NULL))))`

func nullableIntCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "BIGINT", Type: schemadef.TypeInt}
}

func textCol(name string) schemadef.Column {
	return schemadef.Column{Name: name, DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true}
}

// couponsTable is the real control-plane shape: the payload union PLUS the
// single-column CHECK vocabulary on the discriminator, because that is how the
// pattern is actually written and the two have to agree.
func couponsTable() schemadef.Table {
	return schemadef.Table{
		Name:   "coupons",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), textCol("code"), textCol("kind"),
			nullableIntCol("amount_cents"), nullableIntCol("compute_minutes"),
		},
		Indexes: []schemadef.Index{
			{Name: "coupons_code_key", Columns: []string{"code"}, Unique: true},
		},
		Checks: []schemadef.CheckConstraint{
			check("ck_cp_coupons_kind",
				"CHECK ((kind = ANY (ARRAY['wallet_credit'::text, 'compute_minutes'::text])))", "kind"),
			check("ck_cp_coupons_payload_matches_kind", couponsPayloadCheck,
				"kind", "amount_cents", "compute_minutes"),
		},
	}
}

// couponRowOK is the CHECK itself, evaluated in Go. Asserting on it rather
// than on the individual columns is the point: the test states the constraint
// once, the way the schema does, and any placement that satisfies it passes.
func couponRowOK(kind string, amount, minutes *int64) bool {
	wallet := kind == "wallet_credit" && amount != nil && *amount > 0 && minutes == nil
	compute := kind == "compute_minutes" && minutes != nil && *minutes > 0 && amount == nil
	return wallet || compute
}

// seedNullableInt reads a cell that the plan may deliberately leave NULL.
func seedNullableInt(t *testing.T, p *Plan, table, column string, row int) *int64 {
	t.Helper()
	raw, ok := p.SeedValue(table, column, row)
	if !ok {
		return nil // NULL, or not a plain scalar
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("%s.%s row %d = %q, which is not an integer", table, column, row, raw)
	}
	return &n
}

// The SHAPE corpus. The accepted half is the discriminated union as authors
// write it; the refused half is the whole point of the exercise in the other
// direction — a matcher that reached any of those would be claiming a
// placement it cannot demonstrate.
func TestParseUnionBranches_Shapes(t *testing.T) {
	cases := []struct {
		name     string
		def      string
		branches int // 0 means "must be refused"
	}{{
		name:     "the coupons payload union",
		def:      couponsPayloadCheck,
		branches: 2,
	}, {
		name: "three arms",
		def: "CHECK ((((kind = 'a'::text) AND (x IS NOT NULL)) OR ((kind = 'b'::text) AND (y IS NOT NULL)) " +
			"OR ((kind = 'c'::text) AND (z IS NOT NULL))))",
		branches: 3,
	}, {
		name:     "a numeric discriminator",
		def:      "CHECK ((((tier = 1) AND (cents IS NOT NULL)) OR ((tier = 2) AND (minutes IS NOT NULL))))",
		branches: 2,
	}, {
		name: "a boolean discriminator",
		def: "CHECK ((((is_percent = true) AND (percent IS NOT NULL) AND (cents IS NULL)) " +
			"OR ((is_percent = false) AND (cents IS NOT NULL) AND (percent IS NULL))))",
		branches: 2,
	}, {
		name:     "a single-term arm alongside a conjunctive one",
		def:      "CHECK (((kind = 'none'::text) OR ((kind = 'wallet'::text) AND (cents > 0))))",
		branches: 2,
	}, {
		name:     "constraint added NOT VALID is still enforced on inserts",
		def:      "CHECK ((((kind = 'a'::text) AND (x IS NULL)) OR ((kind = 'b'::text) AND (x IS NOT NULL)))) NOT VALID",
		branches: 2,
	}, {
		// ── refused ────────────────────────────────────────────────────
		name: "no top-level OR — a single AND-group is not a union",
		def:  "CHECK (((kind = 'wallet_credit'::text) AND (amount_cents IS NOT NULL)))",
	}, {
		name: "a branch with no discriminator equality",
		def:  "CHECK ((((amount_cents IS NOT NULL)) OR ((compute_minutes IS NOT NULL))))",
	}, {
		name: "a nested OR inside a branch",
		def:  "CHECK ((((kind = 'a'::text) AND ((x IS NULL) OR (y IS NULL))) OR ((kind = 'b'::text) AND (x IS NOT NULL))))",
	}, {
		name: "a negated branch",
		def:  "CHECK ((((kind = 'a'::text) AND (NOT (x IS NULL))) OR ((kind = 'b'::text) AND (x IS NULL))))",
	}, {
		name: "a function call in a branch",
		def:  "CHECK ((((kind = 'a'::text) AND (num_nonnulls(x, y) = 1)) OR ((kind = 'b'::text) AND (x IS NULL))))",
	}, {
		name: "a column-to-column comparison in a branch — that is an ordering, not a union",
		def:  "CHECK ((((kind = 'a'::text) AND (expires_at > issued_at)) OR ((kind = 'b'::text) AND (x IS NULL))))",
	}, {
		name: "an equality against a sibling COLUMN rather than a literal",
		def:  "CHECK ((((kind = other_kind) AND (x IS NULL)) OR ((kind = 'b'::text) AND (x IS NOT NULL))))",
	}, {
		name: "an arithmetic operand",
		def:  "CHECK ((((kind = 'a'::text) AND (total = (subtotal + tax))) OR ((kind = 'b'::text) AND (total IS NULL))))",
	}, {
		name: "an IN list inside a branch",
		def:  "CHECK ((((kind = ANY (ARRAY['a'::text, 'b'::text])) AND (x IS NULL)) OR ((kind = 'c'::text) AND (x IS NOT NULL))))",
	}, {
		name: "an inequality discriminator",
		def:  "CHECK ((((kind <> 'a'::text) AND (x IS NULL)) OR ((kind = 'b'::text) AND (x IS NOT NULL))))",
	}, {
		name: "a comparison against a non-numeric literal",
		def:  "CHECK ((((kind = 'a'::text) AND (started_at > 'epoch'::timestamp)) OR ((kind = 'b'::text) AND (started_at IS NULL))))",
	}, {
		name: "a CASE expression",
		def:  "CHECK (\n CASE\n     WHEN (kind = 'a'::text) THEN (x IS NULL)\n     ELSE true\nEND)",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := checkBody(tc.def)
			if !ok {
				t.Fatalf("checkBody could not strip the CHECK wrapper from %s", tc.def)
			}
			groups, ok := parseUnionBranches(body)
			if tc.branches == 0 {
				if ok {
					t.Fatalf("%s must be REFUSED (forge's placement does not prove it), got %d branch(es)", tc.def, len(groups))
				}
				return
			}
			if !ok {
				t.Fatalf("%s must be read as a discriminated union; it was refused", tc.def)
			}
			if len(groups) != tc.branches {
				t.Errorf("%s: got %d branch(es), want %d", tc.def, len(groups), tc.branches)
			}
			for i, terms := range groups {
				pinned := false
				for _, term := range terms {
					if term.kind == termEq {
						pinned = true
					}
				}
				if !pinned {
					t.Errorf("branch %d carries no discriminator equality — the matcher must not accept it", i)
				}
			}
		})
	}
}

// The plan-level half: the real constraint, evaluated against the real rows.
// Before this pass the plan warned and `forge db seed apply` wrote no coupons
// at all.
func TestUnion_CouponRowsSatisfyTheCheck(t *testing.T) {
	const rows = 8
	tbl := couponsTable()
	p := planFor(t, tbl, rows)
	if warns := p.Warnings(); len(warns) > 0 {
		t.Fatalf("a placeable discriminated union must produce no warning, got:\n  %s", strings.Join(warns, "\n  "))
	}
	if got := p.rowsOf["coupons"]; got != rows {
		t.Fatalf("coupons holds %d row(s), want %d — a capped table is the other way to fail this", got, rows)
	}
	kinds := map[string]int{}
	for row := 0; row < rows; row++ {
		kind, ok := p.SeedValue("coupons", "kind", row)
		if !ok {
			t.Fatalf("row %d: kind is not seeded", row)
		}
		kinds[kind]++
		amount := seedNullableInt(t, p, "coupons", "amount_cents", row)
		minutes := seedNullableInt(t, p, "coupons", "compute_minutes", row)
		if !couponRowOK(kind, amount, minutes) {
			t.Errorf("row %d: (kind=%q, amount_cents=%v, compute_minutes=%v) violates ck_cp_coupons_payload_matches_kind",
				row, kind, amount, minutes)
		}
	}
	// Coverage is the reason the branch is chosen round-robin rather than by
	// hash: a dataset missing an arm leaves that code path with no row.
	for _, want := range []string{"wallet_credit", "compute_minutes"} {
		if kinds[want] == 0 {
			t.Errorf("no seeded row carries kind=%q — every branch of the union must appear in the dataset", want)
		}
	}
}

// One row's columns must all come from the SAME branch. A row assembled from
// two branches satisfies neither, and it is the failure a per-column model
// cannot even see.
func TestUnion_OneRowComesFromOneBranch(t *testing.T) {
	p := planFor(t, couponsTable(), 6)
	for row := 0; row < 6; row++ {
		kind, _ := p.SeedValue("coupons", "kind", row)
		amount := seedNullableInt(t, p, "coupons", "amount_cents", row)
		minutes := seedNullableInt(t, p, "coupons", "compute_minutes", row)
		switch kind {
		case "wallet_credit":
			if amount == nil || minutes != nil {
				t.Errorf("row %d is kind=wallet_credit but carries amount_cents=%v compute_minutes=%v", row, amount, minutes)
			}
		case "compute_minutes":
			if minutes == nil || amount != nil {
				t.Errorf("row %d is kind=compute_minutes but carries amount_cents=%v compute_minutes=%v", row, amount, minutes)
			}
		default:
			t.Errorf("row %d: kind=%q is outside the column's own CHECK vocabulary", row, kind)
		}
	}
}

// Determinism is what the whole planner rests on, and a union placement is a
// new source of per-row state — so the same (schema, config) must still render
// byte-identically, and raising the row count must APPEND rows rather than
// reshuffle the ones already seeded.
func TestUnion_IsDeterministicAndAppendOnly(t *testing.T) {
	tbl := couponsTable()
	a := planFor(t, tbl, 6).Render()
	b := planFor(t, tbl, 6).Render()
	if a != b {
		t.Errorf("two plans over the same schema rendered differently:\n--- first ---\n%s\n--- second ---\n%s", a, b)
	}
	wide := planFor(t, tbl, 12)
	narrow := planFor(t, tbl, 6)
	for row := 0; row < 6; row++ {
		for _, col := range []string{"kind", "amount_cents", "compute_minutes"} {
			x, okX := narrow.SeedValue("coupons", col, row)
			y, okY := wide.SeedValue("coupons", col, row)
			if okX != okY || x != y {
				t.Errorf("row %d %s moved when the row target grew: %q(%t) -> %q(%t)", row, col, x, okX, y, okY)
			}
		}
	}
}

// A schema with no union in it must seed exactly as before — the pass may only
// change the columns its own constraint governs.
func TestUnion_LeavesUnconstrainedColumnsAlone(t *testing.T) {
	plain := planFor(t, schemadef.Table{
		Name: "coupons", PKCols: []string{"id"},
		Columns: []schemadef.Column{idCol(), textCol("code"), textCol("kind"), nullableIntCol("amount_cents")},
	}, 4)
	unioned := planFor(t, schemadef.Table{
		Name: "coupons", PKCols: []string{"id"},
		Columns: []schemadef.Column{idCol(), textCol("code"), textCol("kind"), nullableIntCol("amount_cents")},
		Checks: []schemadef.CheckConstraint{
			check("ck", "CHECK ((((kind = 'a'::text) AND (amount_cents IS NOT NULL)) OR ((kind = 'b'::text) AND (amount_cents IS NULL))))",
				"kind", "amount_cents"),
		},
	}, 4)
	for row := 0; row < 4; row++ {
		x, _ := plain.SeedValue("coupons", "code", row)
		y, _ := unioned.SeedValue("coupons", "code", row)
		if x != y {
			t.Errorf("row %d: code %q became %q — a union may only place the columns it names", row, x, y)
		}
	}
}

// The refusals. Every one of these is a union-SHAPED constraint forge will not
// place, and every one must be NAMED — a silent skip is how a seeder ends up
// writing rows that contradict a rule the schema states.
func TestUnion_UnplaceableConstraintsAreNamed(t *testing.T) {
	unionCheck := func(cols ...string) schemadef.CheckConstraint {
		return check("ck_payload",
			"CHECK ((((kind = 'a'::text) AND (amount_cents IS NOT NULL) AND (amount_cents > 0)) "+
				"OR ((kind = 'b'::text) AND (amount_cents IS NULL))))", cols...)
	}
	cases := []struct {
		name    string
		table   schemadef.Table
		wantWhy string
	}{{
		name: "the discriminator is UNIQUE, so the distinct-value assignment owns it",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("kind"), nullableIntCol("amount_cents")},
			Indexes: []schemadef.Index{{Name: "u", Columns: []string{"kind"}, Unique: true}},
			Checks:  []schemadef.CheckConstraint{unionCheck("kind", "amount_cents")},
		},
		wantWhy: "kind is UNIQUE",
	}, {
		name: "a payload column is a foreign key",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("kind"), nullableIntCol("amount_cents")},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "amount_cents", RefTable: "coupons", RefColumn: "id", Name: "fk"},
			},
			Checks: []schemadef.CheckConstraint{unionCheck("kind", "amount_cents")},
		},
		wantWhy: "amount_cents is a foreign key",
	}, {
		name: "the discriminator is the primary key",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"kind"},
			Columns: []schemadef.Column{
				{Name: "kind", DeclType: "TEXT", Type: schemadef.TypeString, NotNull: true, IsPK: true},
				nullableIntCol("amount_cents"),
			},
			Checks: []schemadef.CheckConstraint{unionCheck("kind", "amount_cents")},
		},
		wantWhy: "kind is a key column",
	}, {
		name: "a second multi-column constraint spans the same columns",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("kind"), nullableIntCol("amount_cents")},
			Checks: []schemadef.CheckConstraint{
				unionCheck("kind", "amount_cents"),
				check("ck_exclusive", "CHECK ((num_nonnulls(kind, amount_cents) = 1))", "kind", "amount_cents"),
			},
		},
		wantWhy: `constraint "ck_exclusive" spans`,
	}, {
		name: "every branch contradicts the column's own CHECK vocabulary",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("kind"), nullableIntCol("amount_cents")},
			Checks: []schemadef.CheckConstraint{
				unionCheck("kind", "amount_cents"),
				check("ck_kind", "CHECK ((kind = ANY (ARRAY['x'::text, 'y'::text])))", "kind"),
			},
		},
		wantWhy: "no branch of it is satisfiable",
	}, {
		name: "the discriminator is pinned to a literal of the wrong type",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), intCol("kind"), nullableIntCol("amount_cents")},
			Checks:  []schemadef.CheckConstraint{unionCheck("kind", "amount_cents")},
		},
		wantWhy: "to the string literal",
	}, {
		name: "the expression names a column postgres does not report on the constraint",
		table: schemadef.Table{
			Name: "coupons", PKCols: []string{"id"},
			Columns: []schemadef.Column{idCol(), textCol("kind"), nullableIntCol("amount_cents")},
			Checks:  []schemadef.CheckConstraint{unionCheck("kind", "something_else")},
		},
		wantWhy: "which postgres does not report",
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := planFor(t, tc.table, 4)
			joined := strings.Join(p.Warnings(), "\n")
			if !strings.Contains(joined, "ck_payload") || !strings.Contains(joined, tc.wantWhy) {
				t.Fatalf("the plan must name constraint %q and why it could not be placed (%q); got:\n%s",
					"ck_payload", tc.wantWhy, joined)
			}
			if !strings.Contains(joined, "seeded rows satisfy it only by chance") {
				t.Errorf("the refusal must keep the existing consequence clause; got:\n%s", joined)
			}
		})
	}
}

// A constraint the union matcher does NOT recognize must keep reaching the
// ordering pass's warning, unchanged and exactly once. Widening one matcher
// must not quietly swallow the constraints the other one reports.
func TestUnion_UnrecognizedShapeKeepsTheOrderingWarning(t *testing.T) {
	p := planFor(t, schemadef.Table{
		Name: "docs", PKCols: []string{"id"},
		Columns: []schemadef.Column{idCol(), timeCol("signed_at"), timeCol("voided_at")},
		Checks: []schemadef.CheckConstraint{
			check("docs_exclusive", "CHECK ((num_nonnulls(signed_at, voided_at) = 1))", "signed_at", "voided_at"),
		},
	}, 3)
	warns := p.Warnings()
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `docs constraint "docs_exclusive" spans two columns and forge cannot place its values (not a two-column ordering comparison)`) {
		t.Fatalf("an unrecognized multi-column CHECK must keep the existing warning verbatim; got:\n%s", joined)
	}
	if n := strings.Count(joined, "docs_exclusive"); n != 1 {
		t.Errorf("one line of SQL must collect one warning, got %d mentioning docs_exclusive:\n%s", n, joined)
	}
}

// A union CHECK must not double-warn either: the union pass speaks for the
// constraint, so the ordering pass has to step over it.
func TestUnion_PlacedConstraintDrawsNoOrderingWarning(t *testing.T) {
	p := planFor(t, couponsTable(), 4)
	if joined := strings.Join(p.Warnings(), "\n"); strings.Contains(joined, "ordering comparison") {
		t.Errorf("a placed union must not also be reported as an unreadable ordering constraint; got:\n%s", joined)
	}
}

// The overlay is a preference and a CHECK is not, so the placement wins — but
// an author whose pin silently stopped applying has no way to find out except
// this line.
func TestUnion_OverriddenVocabEntryIsReported(t *testing.T) {
	tbl := couponsTable()
	tables := []schemadef.Table{tbl}
	p, err := BuildPlan(tables, PoolsFromTables(tables), Config{Rows: 4, Salt: 1})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	p.ApplyVocab(&Vocab{Columns: map[string][]string{
		"coupons.kind": {"wallet_credit"},
	}})
	joined := strings.Join(p.Warnings(), "\n")
	if !strings.Contains(joined, "coupons.kind") || !strings.Contains(joined, "the CHECK wins") {
		t.Errorf("an overlay entry a union placement overrides must be reported; got:\n%s", joined)
	}
}

// couponsMigration is the real control-plane shape, reduced to the two
// constraints under test.
const couponsMigration = `
CREATE TABLE coupons (
    id TEXT PRIMARY KEY,
    code TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('wallet_credit', 'compute_minutes')),
    amount_cents BIGINT,
    compute_minutes BIGINT
);
ALTER TABLE coupons
    ADD CONSTRAINT ck_cp_coupons_payload_matches_kind
    CHECK (
        (kind = 'wallet_credit'
            AND amount_cents IS NOT NULL AND amount_cents > 0
            AND compute_minutes IS NULL)
        OR
        (kind = 'compute_minutes'
            AND compute_minutes IS NOT NULL AND compute_minutes > 0
            AND amount_cents IS NULL)
    );
`

// Real postgres is the judge. It enforces the union on every row, so the
// INSERT landing IS the proof — and it is also the proof that the constraint
// text the matcher was written against is the text postgres hands back.
func TestMaterialize_DiscriminatedUnionOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(couponsMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(couponsMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 10, Salt: 1}); err != nil {
		t.Fatalf("%v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM coupons`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Errorf("coupons holds %d row(s), want 10 — a table skipped is the other way to fail this", n)
	}
	// Both arms must exist: a dataset with only wallet coupons cannot
	// exercise the compute-redemption path at all.
	var kinds int
	if err := db.QueryRow(`SELECT count(DISTINCT kind) FROM coupons`).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if kinds != 2 {
		t.Errorf("seeded coupons carry %d distinct kind(s), want 2 — every branch of the union must appear", kinds)
	}
}
