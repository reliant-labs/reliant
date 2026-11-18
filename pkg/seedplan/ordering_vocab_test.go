// File: pkg/seedplan/ordering_vocab_test.go
//
// The ordering pass places a ranked column by ARITHMETIC — base + rank — and
// for a long time consulted only the column's range CHECK on the way. The
// author's own declaration in db/seeds/vocab.yaml was invisible to it, so a
// pairing CHECK silently defeated an explicit range:
//
//	invoices.amount_cents:      {min: 500000, max: 2900000, step: 1000}
//	invoices.amount_paid_cents: {min: 0,      max: 480000,  step: 1000}
//	CHECK (amount_paid_cents <= amount_cents)
//
// Measured twice, on two dogfood runs of the same schema: every invoice came
// out at amount_cents = amount_paid_cents + 1, minimum observed value 1 —
// three orders of magnitude below the declared floor — and the app's
// collections screen showed twenty invoices with a one-cent balance. Schema
// valid, product useless.
//
// The control in those runs was clean: payments.amount_cents carried a range
// and NO pairing CHECK, and it was honored exactly. So ranges worked; a
// paired CHECK defeated them.
//
// These tests pin both halves of the answer. When the declaration and the
// constraint are JOINTLY satisfiable the placement honors both, because in
// the case above they plainly are. When they genuinely conflict the placement
// still satisfies the CHECK — a rejected INSERT rolls back every table that
// already succeeded — but it says so by name rather than quietly winning.

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

// invoiceTable is the measured shape: two money columns paired by a CHECK,
// with the higher one the RANKED column the ordering pass places.
func invoiceTable() schemadef.Table {
	return schemadef.Table{
		Name:   "invoices",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), intCol("amount_cents"), intCol("amount_paid_cents"),
		},
		Checks: []schemadef.CheckConstraint{
			// Verbatim from the measured run, as postgres renders it.
			check("invoices_not_overpaid",
				"CHECK ((amount_paid_cents <= amount_cents))",
				"amount_paid_cents", "amount_cents"),
		},
	}
}

// planWithVocab builds the plan a real seed builds for one table, then
// overlays a vocab.yaml written to disk — the same LoadVocab path the CLI
// takes, so the {min,max,step} expansion is exercised rather than simulated.
func planWithVocab(t *testing.T, table schemadef.Table, rows int, vocabYAML string) *Plan {
	t.Helper()
	p := planFor(t, table, rows)
	v, err := LoadVocab(writeVocab(t, vocabYAML))
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	if v == nil {
		t.Fatal("vocab overlay parsed as empty — this test would be vacuous")
	}
	p.ApplyVocab(v)
	return p
}

// seedInt reads a seeded cell as an integer.
func seedInt(t *testing.T, p *Plan, table, column string, row int) int64 {
	t.Helper()
	raw, ok := p.SeedValue(table, column, row)
	if !ok {
		t.Fatalf("%s.%s row %d has no seeded scalar value", table, column, row)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("%s.%s row %d = %q, which is not an integer: %v", table, column, row, raw, err)
	}
	return n
}

// The defect itself. A column with a declared range AND a pairing CHECK must
// land inside the declared range — and must still satisfy the CHECK, because
// correct-but-out-of-range and in-range-but-rejected are both failures.
func TestOrdering_DeclaredRangeIsHonoredUnderPairingCheck(t *testing.T) {
	const rows = 20
	p := planWithVocab(t, invoiceTable(), rows, `
columns:
  invoices.amount_cents:      {min: 500000, max: 2900000, step: 1000}
  invoices.amount_paid_cents: {min: 0,      max: 480000,  step: 1000}
`)

	var lo, hi int64
	for i := range rows {
		amount := seedInt(t, p, "invoices", "amount_cents", i)
		paid := seedInt(t, p, "invoices", "amount_paid_cents", i)
		if i == 0 || amount < lo {
			lo = amount
		}
		if amount > hi {
			hi = amount
		}
		if amount < 500000 || amount > 2900000 {
			t.Errorf("row %d: amount_cents = %d, outside the declared range [500000, 2900000] — "+
				"the pairing CHECK discarded the author's declaration", i, amount)
		}
		if paid < 0 || paid > 480000 {
			t.Errorf("row %d: amount_paid_cents = %d, outside the declared range [0, 480000]", i, paid)
		}
		if paid > amount {
			t.Errorf("row %d: amount_paid_cents (%d) > amount_cents (%d), violating invoices_not_overpaid",
				i, paid, amount)
		}
	}
	t.Logf("amount_cents observed range: [%d, %d] (declared [500000, 2900000])", lo, hi)

	// The product consequence the runs actually reported: a balance column
	// that is $0.01 on every row. Honoring the declaration is what makes the
	// balances spread.
	balances := map[int64]bool{}
	for i := range rows {
		balances[seedInt(t, p, "invoices", "amount_cents", i)-
			seedInt(t, p, "invoices", "amount_paid_cents", i)] = true
	}
	if len(balances) < 2 {
		t.Errorf("every seeded invoice has the same balance (%v) — the pairing CHECK is still pinning "+
			"amount_cents to amount_paid_cents + 1", balances)
	}
}

// The clean control from the measured runs, kept as a test: a ranged column
// with NO pairing CHECK was always honored, and must stay honored. A fix that
// only works under a pairing constraint would pass the test above while
// regressing the ordinary case.
func TestOrdering_RangeWithoutPairingCheckStaysHonored(t *testing.T) {
	const rows = 20
	table := schemadef.Table{
		Name:    "payments",
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{idCol(), intCol("amount_cents")},
	}
	p := planWithVocab(t, table, rows, `
columns:
  payments.amount_cents: {min: 25000, max: 480000, step: 1000}
`)
	var lo, hi int64
	for i := range rows {
		v := seedInt(t, p, "payments", "amount_cents", i)
		if i == 0 || v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
		if v < 25000 || v > 480000 {
			t.Errorf("row %d: amount_cents = %d, outside the declared range [25000, 480000]", i, v)
		}
	}
	t.Logf("amount_cents observed range: [%d, %d] (declared [25000, 480000])", lo, hi)
}

// A chain, to prove the placement is still a PROOF and not a two-column
// special case: `min <= mid <= max` with a declared range on each. Every
// edge must hold and every value must sit in its own declared range.
func TestOrdering_DeclaredRangesHonoredAlongAChain(t *testing.T) {
	const rows = 12
	table := schemadef.Table{
		Name:   "tiers",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			idCol(), intCol("low_cents"), intCol("mid_cents"), intCol("high_cents"),
		},
		Checks: []schemadef.CheckConstraint{
			check("tiers_ordered",
				"CHECK (((low_cents <= mid_cents) AND (mid_cents <= high_cents)))",
				"low_cents", "mid_cents", "high_cents"),
		},
	}
	p := planWithVocab(t, table, rows, `
columns:
  tiers.low_cents:  {min: 1000,   max: 9000,   step: 500}
  tiers.mid_cents:  {min: 20000,  max: 90000,  step: 500}
  tiers.high_cents: {min: 200000, max: 900000, step: 500}
`)
	for i := range rows {
		low := seedInt(t, p, "tiers", "low_cents", i)
		mid := seedInt(t, p, "tiers", "mid_cents", i)
		high := seedInt(t, p, "tiers", "high_cents", i)
		if low < 1000 || low > 9000 {
			t.Errorf("row %d: low_cents = %d outside declared [1000, 9000]", i, low)
		}
		if mid < 20000 || mid > 90000 {
			t.Errorf("row %d: mid_cents = %d outside declared [20000, 90000]", i, mid)
		}
		if high < 200000 || high > 900000 {
			t.Errorf("row %d: high_cents = %d outside declared [200000, 900000]", i, high)
		}
		if !(low <= mid && mid <= high) {
			t.Errorf("row %d: %d <= %d <= %d does not hold — tiers_ordered is violated", i, low, mid, high)
		}
	}
}

// When the two genuinely conflict — a declared range that sits ENTIRELY at or
// below the value the pairing CHECK forces — forge must still write rows the
// CHECK accepts (a rejected INSERT rolls back every table that already
// succeeded), and must say which declaration it could not honor and why. The
// silent override is the defect; a loud one is the floor.
func TestOrdering_ConflictingRangeIsRefusedByName(t *testing.T) {
	const rows = 10
	p := planWithVocab(t, invoiceTable(), rows, `
columns:
  invoices.amount_cents:      {min: 100,    max: 900,    step: 100}
  invoices.amount_paid_cents: {min: 500000, max: 600000, step: 1000}
`)
	joined := strings.Join(p.Warnings(), "\n  ")
	for _, want := range []string{
		"invoices.amount_cents", // WHICH declaration was not honored
		"invoices_not_overpaid", // the constraint that contradicts it
		"db/seeds/vocab.yaml",   // where the author fixes it
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the refusal must mention %q so the author can act on it; got:\n  %s", want, joined)
		}
	}
	// And the rows still satisfy the CHECK — refusing loudly is not licence
	// to write data postgres rejects.
	for i := range rows {
		amount := seedInt(t, p, "invoices", "amount_cents", i)
		paid := seedInt(t, p, "invoices", "amount_paid_cents", i)
		if paid > amount {
			t.Errorf("row %d: amount_paid_cents (%d) > amount_cents (%d) — the refusal must not cost "+
				"the constraint the placement exists to satisfy", i, paid, amount)
		}
	}
}

// invoiceVocabMigration is the measured shape, as SQL: the pairing CHECK that
// defeated the declared range, plus the clean control (a ranged column on
// another table with no pairing CHECK) in the same schema.
const invoiceVocabMigration = `
CREATE TABLE invoices (
    id TEXT PRIMARY KEY,
    amount_cents BIGINT NOT NULL,
    amount_paid_cents BIGINT NOT NULL,
    balance_cents BIGINT GENERATED ALWAYS AS (amount_cents - amount_paid_cents) STORED,
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())
);
ALTER TABLE invoices ADD CONSTRAINT invoices_not_overpaid CHECK (amount_paid_cents <= amount_cents);

CREATE TABLE payments (
    id TEXT PRIMARY KEY,
    amount_cents BIGINT NOT NULL
);
`

const invoiceVocabYAML = `
columns:
  invoices.amount_cents:      {min: 500000, max: 2900000, step: 1000}
  invoices.amount_paid_cents: {min: 0,      max: 480000,  step: 1000}
  payments.amount_cents:      {min: 25000,  max: 480000,  step: 1000}
`

// Real postgres is the judge. It enforces invoices_not_overpaid on every row,
// so the INSERT landing IS the proof the placement satisfied it — and the
// min/max queried back out of the table is the proof the declaration survived.
// Both halves matter: correct-but-out-of-range was the defect, and
// in-range-but-rejected would abort the whole transactional seed.
func TestMaterialize_DeclaredRangeSurvivesPairingCheckOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(invoiceVocabMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"),
		[]byte(invoiceVocabMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	// VocabPath reads db/seeds/vocab.yaml as a sibling of the migrations dir,
	// so the overlay goes through the very path `forge db seed` takes.
	seeds := filepath.Join(filepath.Dir(dir), "seeds")
	if err := os.MkdirAll(seeds, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seeds, "vocab.yaml"), []byte(invoiceVocabYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 20, Salt: 1}); err != nil {
		t.Fatalf("%v", err)
	}

	var lo, hi, paidLo, paidHi int64
	if err := db.QueryRow(`SELECT min(amount_cents), max(amount_cents),
	           min(amount_paid_cents), max(amount_paid_cents) FROM invoices`).
		Scan(&lo, &hi, &paidLo, &paidHi); err != nil {
		t.Fatal(err)
	}
	t.Logf("invoices.amount_cents      seeded [%d, %d] (declared [500000, 2900000])", lo, hi)
	t.Logf("invoices.amount_paid_cents seeded [%d, %d] (declared [0, 480000])", paidLo, paidHi)
	if lo < 500000 || hi > 2900000 {
		t.Errorf("amount_cents seeded [%d, %d], outside the declared [500000, 2900000]", lo, hi)
	}
	if paidLo < 0 || paidHi > 480000 {
		t.Errorf("amount_paid_cents seeded [%d, %d], outside the declared [0, 480000]", paidLo, paidHi)
	}

	// The clean control, in the same run: a ranged column with no pairing
	// CHECK was always honored and must stay honored.
	var payLo, payHi int64
	if err := db.QueryRow(`SELECT min(amount_cents), max(amount_cents) FROM payments`).
		Scan(&payLo, &payHi); err != nil {
		t.Fatal(err)
	}
	t.Logf("payments.amount_cents      seeded [%d, %d] (declared [25000, 480000])", payLo, payHi)
	if payLo < 25000 || payHi > 480000 {
		t.Errorf("the unpaired control regressed: payments.amount_cents seeded [%d, %d], "+
			"outside the declared [25000, 480000]", payLo, payHi)
	}

	// The product symptom: twenty invoices all showing a $0.01 balance.
	var balances int
	if err := db.QueryRow(`SELECT count(DISTINCT balance_cents) FROM invoices`).Scan(&balances); err != nil {
		t.Fatal(err)
	}
	if balances < 2 {
		t.Errorf("every invoice has the same balance — amount_cents is still pinned to amount_paid_cents + 1")
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM invoices`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 20 {
		t.Errorf("invoices holds %d row(s), want 20 — a table skipped is the other way to fail this", rows)
	}
}

// A schema with no vocabulary overlay at all must place exactly as before.
// The ladder below replaces the old base+rank arithmetic, and an unpooled
// column has to come out at the identical value or every existing dataset
// shifts under a fix that was supposed to be invisible to it.
func TestOrdering_UnpooledPlacementIsUnchanged(t *testing.T) {
	const rows = 8
	p := planFor(t, invoiceTable(), rows)
	for i := range rows {
		paid := seedInt(t, p, "invoices", "amount_paid_cents", i)
		amount := seedInt(t, p, "invoices", "amount_cents", i)
		if amount != paid+1 {
			t.Errorf("row %d: amount_cents = %d, want %d (the natural base plus one rank step)",
				i, amount, paid+1)
		}
	}
}
