package seedplan

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// diamondSchema is the shape the detector exists for: orders references
// patients directly AND reaches patients again through prescriptions. Both
// foreign keys are NOT NULL and individually satisfiable; the invariant lives
// only at the join. `decl` is the comment on orders' patient_id constraint —
// the declaration under test.
func diamondSchema(decl string) []schemadef.Table {
	patients := schemadef.Table{
		Name:   "patients",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("name", schemadef.TypeString, true, false),
		},
	}
	prescriptions := schemadef.Table{
		Name:   "prescriptions",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("patient_id", schemadef.TypeString, true, false),
			col("dosage", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "patient_id", RefTable: "patients", RefColumn: "id", Name: "prescriptions_patient_id_fkey"},
		},
	}
	orders := schemadef.Table{
		Name:   "orders",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("patient_id", schemadef.TypeString, true, false),
			col("prescription_id", schemadef.TypeString, true, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "patient_id", RefTable: "patients", RefColumn: "id", Name: "orders_patient_id_fkey", Comment: decl},
			{Column: "prescription_id", RefTable: "prescriptions", RefColumn: "id", Name: "orders_prescription_id_fkey"},
		},
	}
	return []schemadef.Table{orders, prescriptions, patients}
}

// countDisagreeing recomputes the diamond independently of the resolver,
// through the PUBLIC seeded values (SeedValue is what Render/Apply emit). If
// this and the plan ever disagree, one of them is lying.
func countDisagreeing(t *testing.T, p *Plan, rows int) int {
	t.Helper()
	rxPatient := map[string]string{}
	for j := 0; j < rows; j++ {
		id, ok1 := p.SeedValue("prescriptions", "id", j)
		pid, ok2 := p.SeedValue("prescriptions", "patient_id", j)
		if ok1 && ok2 {
			rxPatient[id] = pid
		}
	}
	n := 0
	for i := 0; i < rows; i++ {
		direct, ok1 := p.SeedValue("orders", "patient_id", i)
		rxID, ok2 := p.SeedValue("orders", "prescription_id", i)
		if !ok1 || !ok2 {
			continue
		}
		via, ok := rxPatient[rxID]
		if !ok {
			t.Fatalf("orders row %d references prescription %q, which no seeded row carries", i, rxID)
		}
		if direct != via {
			n++
		}
	}
	return n
}

// TestDiamond_UndeclaredRefuses is the regression for the finding. Left to
// itself the seeder picks each foreign-key edge independently, so the two
// routes to patients disagree — and those rows were handed to fan-out units as
// verification data for a rule they contradict.
//
// forge does not guess which route wins: it REFUSES, and the refusal is a
// runbook. The disagreement is cross-checked against an independent walk of
// the seeded values so the test cannot pass by agreeing with a broken detector.
func TestDiamond_UndeclaredRefuses(t *testing.T) {
	const rows = 20
	p := buildOrFail(t, diamondSchema(""), Config{Rows: rows, Salt: 1})

	want := countDisagreeing(t, p, rows)
	if want == 0 {
		t.Fatalf("fixture is not exercising the defect: independent walk found 0 disagreeing rows")
	}

	err := p.Validate()
	if err == nil {
		t.Fatal("an undeclared diamond must REFUSE the seed — a report would leave known-incoherent fixtures behind")
	}
	var refusal *UndeclaredDiamondError
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal must be an *UndeclaredDiamondError so callers can recognise it; got %T", err)
	}
	msg := err.Error()

	// The runbook names what was expected, what was found, and the literal fix.
	for _, want := range []string{
		"orders.patient_id -> patients.id",
		"orders.prescription_id -> prescriptions.patient_id",
		"COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref derived-from=prescription_id';",
		"COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref authoritative';",
		"COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref independent';",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q:\n%s", want, msg)
		}
	}
	// It names the rows, by the primary key a user can query with.
	if !strings.Contains(msg, "disagree in") {
		t.Errorf("refusal must say how many rows disagree:\n%s", msg)
	}
	id, ok := p.SeedValue("orders", "id", firstDisagreeingRow(t, p, rows))
	if !ok {
		t.Fatal("orders.id must be a seeded scalar")
	}
	if !strings.Contains(msg, "orders.id="+id) {
		t.Errorf("refusal must name the first disagreeing row (%s):\n%s", id, msg)
	}
	// And it does not choose.
	for _, banned := range []string{"forge will use", "forge chose", "corrected"} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(banned)) {
			t.Errorf("the refusal must not pick a winner (%q):\n%s", banned, msg)
		}
	}
}

// firstDisagreeingRow returns the lowest orders row whose two routes name
// different patients, computed independently of the resolver.
func firstDisagreeingRow(t *testing.T, p *Plan, rows int) int {
	t.Helper()
	rxPatient := map[string]string{}
	for j := 0; j < rows; j++ {
		id, ok1 := p.SeedValue("prescriptions", "id", j)
		pid, ok2 := p.SeedValue("prescriptions", "patient_id", j)
		if ok1 && ok2 {
			rxPatient[id] = pid
		}
	}
	for i := 0; i < rows; i++ {
		direct, _ := p.SeedValue("orders", "patient_id", i)
		rxID, _ := p.SeedValue("orders", "prescription_id", i)
		if direct != rxPatient[rxID] {
			return i
		}
	}
	t.Fatal("no disagreeing row")
	return 0
}

// TestDiamond_DerivedFromMakesRoutesAgree pins the first verdict: the
// transitive route is authoritative, so the direct column is seeded FROM it and
// every row agrees.
func TestDiamond_DerivedFromMakesRoutesAgree(t *testing.T) {
	const rows = 20
	p := buildOrFail(t, diamondSchema("forge:ref derived-from=prescription_id"), Config{Rows: rows, Salt: 1})

	if err := p.Validate(); err != nil {
		t.Fatalf("a declared diamond must not refuse: %v", err)
	}
	if n := countDisagreeing(t, p, rows); n != 0 {
		t.Errorf("derived-from must make every row agree; %d of %d still disagree", n, rows)
	}
	// The derivation is real, not a relabelling: the seeded patient is the
	// prescription's patient, which is NOT what an independent pick produced.
	if countDisagreeing(t, buildOrFail(t, diamondSchema(""), Config{Rows: rows, Salt: 1}), rows) == 0 {
		t.Fatal("the undeclared fixture must disagree, or this test proves nothing")
	}
}

// TestDiamond_AuthoritativeNarrowsTheOtherEdge pins the second verdict: the
// direct column is the truth, so the OTHER edge is narrowed to prescriptions
// that belong to the patient the order already names. The invariant holds from
// the other side.
func TestDiamond_AuthoritativeNarrowsTheOtherEdge(t *testing.T) {
	const rows = 20
	p := buildOrFail(t, diamondSchema("forge:ref authoritative"), Config{Rows: rows, Salt: 1})

	if err := p.Validate(); err != nil {
		t.Fatalf("a declared diamond must not refuse: %v", err)
	}
	if n := countDisagreeing(t, p, rows); n != 0 {
		t.Errorf("authoritative must make every row agree; %d of %d still disagree", n, rows)
	}
	// The narrowing is the point: the order's patient is drawn from patients a
	// prescription actually reaches, so more than one distinct patient still
	// appears (a resolution that collapsed every order onto one patient would
	// "agree" while destroying the dataset).
	distinct := map[string]bool{}
	for i := 0; i < rows; i++ {
		if v, ok := p.SeedValue("orders", "patient_id", i); ok {
			distinct[v] = true
		}
	}
	if len(distinct) < 2 {
		t.Errorf("authoritative narrowed orders.patient_id to %d distinct value(s) — the dataset lost its variety", len(distinct))
	}
}

// TestDiamond_IndependentIsSilent pins the third verdict, and it is the one
// that must exist: an order CAN ship to an address belonging to someone else.
// Declaring that seeds each edge on its own and never asks again.
func TestDiamond_IndependentIsSilent(t *testing.T) {
	const rows = 20
	p := buildOrFail(t, diamondSchema("forge:ref independent"), Config{Rows: rows, Salt: 1})

	if err := p.Validate(); err != nil {
		t.Fatalf("a declared-independent diamond must not refuse: %v", err)
	}
	// Independent means independent: the values are the ones the undeclared
	// plan produced, disagreements and all.
	bare := buildOrFail(t, diamondSchema(""), Config{Rows: rows, Salt: 1})
	for i := 0; i < rows; i++ {
		got, _ := p.SeedValue("orders", "patient_id", i)
		want, _ := bare.SeedValue("orders", "patient_id", i)
		if got != want {
			t.Fatalf("row %d: independent must not change the value (%q vs %q)", i, got, want)
		}
	}
}

// TestDiamond_CommentProseIsIgnored keeps the marker from swallowing a human
// note: a constraint comment is a normal place to write prose, and only the
// `forge:ref ` clause is a declaration.
func TestDiamond_CommentProseIsIgnored(t *testing.T) {
	p := buildOrFail(t, diamondSchema("the patient who placed the order"), Config{Rows: 20, Salt: 1})
	if err := p.Validate(); err == nil {
		t.Error("prose is not a declaration — the diamond is still undeclared and must refuse")
	}
	p = buildOrFail(t, diamondSchema("owner of the order; forge:ref independent"), Config{Rows: 20, Salt: 1})
	if err := p.Validate(); err != nil {
		t.Errorf("a declaration alongside prose must still be read: %v", err)
	}
}

// TestDiamond_UniqueColumnCannotBeResolved pins the one declaration forge
// refuses to honour. A UNIQUE foreign key is a 1-1 relationship — the seeder
// gives it a distinct parent per row on purpose — so deriving it would put two
// rows on one parent and abort the whole transaction. A refusal naming the
// reason beats a seed that fails inside postgres.
func TestDiamond_UniqueColumnCannotBeResolved(t *testing.T) {
	tables := diamondSchema("forge:ref derived-from=prescription_id")
	for i := range tables {
		if tables[i].Name == "orders" {
			tables[i].Indexes = []schemadef.Index{
				{Name: "orders_patient_id_key", Columns: []string{"patient_id"}, Unique: true},
			}
		}
	}
	p := buildOrFail(t, tables, Config{Rows: 20, Salt: 1})

	err := p.Validate()
	if err == nil {
		t.Fatal("deriving a UNIQUE column would violate that constraint; forge must refuse instead of trying")
	}
	if !strings.Contains(err.Error(), "cannot be honoured") || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("the refusal must say WHY the declaration cannot hold:\n%v", err)
	}
	// And it really did not derive: the plan still renders, and the 1-1
	// assignment is intact.
	seen := map[string]bool{}
	for i := 0; i < p.rowsOf["orders"]; i++ {
		v, ok := p.SeedValue("orders", "patient_id", i)
		if !ok {
			continue
		}
		if seen[v] {
			t.Fatalf("row %d repeated patient %q — the UNIQUE 1-1 assignment was overwritten by the derivation", i, v)
		}
		seen[v] = true
	}
}

// TestDiamond_NoDiamondIsSilent is the other half of the guard. A child with
// two parents that never meet carries no join invariant, so there is nothing to
// declare — a refusal here would be forge demanding a decision that does not
// exist.
func TestDiamond_NoDiamondIsSilent(t *testing.T) {
	tables := diamondSchema("")
	for i := range tables {
		if tables[i].Name == "prescriptions" {
			tables[i].ForeignKeys = nil
			for j := range tables[i].Columns {
				if tables[i].Columns[j].Name == "patient_id" {
					tables[i].Columns[j].Name = "note"
				}
			}
		}
	}
	p := buildOrFail(t, tables, Config{Rows: 20, Salt: 1})
	if err := p.Validate(); err != nil {
		t.Errorf("two unrelated parents are not a diamond: %v", err)
	}
}

// TestDiamond_CoherentByAccidentIsSilent pins "silence is meaningful": a
// diamond whose routes CANNOT disagree — one patient to point at — needs no
// decision from anyone, and demanding one would be nagging.
func TestDiamond_CoherentByAccidentIsSilent(t *testing.T) {
	p := buildOrFail(t, diamondSchema(""), Config{Rows: 20, Salt: 1, RowsPerTable: map[string]int{"patients": 1}})
	if err := p.Validate(); err != nil {
		t.Errorf("one parent row means the two routes always agree: %v", err)
	}
}

// TestDiamond_OneRowPlansAreNeverBlocked pins where the gate is. `forge
// generate` builds one-row plans for the entity factories; a decision that only
// affects a real dataset must not block codegen.
func TestDiamond_OneRowPlansAreNeverBlocked(t *testing.T) {
	p := buildOrFail(t, diamondSchema(""), Config{Rows: 1, Salt: 0})
	if err := p.Validate(); err != nil {
		t.Errorf("a one-row plan cannot carry a disagreement and must never refuse: %v", err)
	}
}

// TestDiamond_DeterministicUnderDeclaration keeps the seeder's core promise:
// same (schema, config) renders byte-identically, resolution included.
func TestDiamond_DeterministicUnderDeclaration(t *testing.T) {
	for _, decl := range []string{"forge:ref derived-from=prescription_id", "forge:ref authoritative", "forge:ref independent"} {
		cfg := Config{Rows: 12, Salt: 3}
		a := buildOrFail(t, diamondSchema(decl), cfg).Render()
		b := buildOrFail(t, diamondSchema(decl), cfg).Render()
		if a != b {
			t.Errorf("%s: render not byte-identical across runs", decl)
		}
	}
}

// TestDiamond_EndToEndOnRealPostgres exercises the whole loop the way a user
// meets it: undeclared migrations refuse to seed, the declaration is applied as
// the plain SQL the refusal printed, and postgres — not forge's model of itself
// — confirms the join agrees.
func TestDiamond_EndToEndOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupDiamondDB(t, "")

	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	// The constraint name and comment survive introspection — the declaration
	// has nowhere to live otherwise.
	if got := fkNamed(t, plan, "orders", "patient_id").Name; got != "orders_patient_id_fkey" {
		t.Fatalf("foreign-key constraint name must be introspected; got %q", got)
	}
	if _, err := Apply(ctx, db, plan); err == nil {
		t.Fatal("Apply must refuse an undeclared diamond rather than write incoherent rows")
	} else if !strings.Contains(err.Error(), "forge:ref derived-from=prescription_id") {
		t.Fatalf("the refusal must carry the paste-ready statement: %v", err)
	}
	if n := countRows(t, ctx, db, "orders"); n != 0 {
		t.Fatalf("a refused seed must write nothing; orders has %d row(s)", n)
	}

	// Declare it, exactly as the runbook said.
	db2, dir2 := setupDiamondDB(t, "COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref derived-from=prescription_id';\n")
	plan2, err := BuildLivePlan(ctx, db2, dir2, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("BuildLivePlan (declared): %v", err)
	}
	if got := fkNamed(t, plan2, "orders", "patient_id").Comment; !strings.Contains(got, "forge:ref") {
		t.Fatalf("the COMMENT must reach the plan through introspection; got %q", got)
	}
	if _, err := Apply(ctx, db2, plan2); err != nil {
		t.Fatalf("Apply (declared): %v", err)
	}

	var disagreeing, total int
	if err := db2.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE o.patient_id <> r.patient_id), count(*)
		FROM orders o JOIN prescriptions r ON r.id = o.prescription_id
	`).Scan(&disagreeing, &total); err != nil {
		t.Fatalf("join query: %v", err)
	}
	if total == 0 {
		t.Fatal("no rows joined — the test proves nothing")
	}
	if disagreeing != 0 {
		t.Errorf("postgres says %d of %d seeded orders still name a different patient than their prescription", disagreeing, total)
	}
}

func fkNamed(t *testing.T, p *Plan, table, column string) schemadef.ForeignKey {
	t.Helper()
	for _, fk := range p.byName[table].ForeignKeys {
		if fk.Column == column {
			return fk
		}
	}
	t.Fatalf("no foreign key on %s.%s", table, column)
	return schemadef.ForeignKey{}
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

const diamondMigration = `
CREATE TABLE patients (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE prescriptions (
    id TEXT PRIMARY KEY,
    patient_id TEXT NOT NULL,
    dosage TEXT NOT NULL,
    CONSTRAINT prescriptions_patient_id_fkey FOREIGN KEY (patient_id) REFERENCES patients(id)
);

CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    patient_id TEXT NOT NULL,
    prescription_id TEXT NOT NULL,
    CONSTRAINT orders_patient_id_fkey FOREIGN KEY (patient_id) REFERENCES patients(id),
    CONSTRAINT orders_prescription_id_fkey FOREIGN KEY (prescription_id) REFERENCES prescriptions(id)
);
`

func setupDiamondDB(t *testing.T, declaration string) (*sql.DB, string) {
	t.Helper()
	migration := diamondMigration + declaration
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(migration); err != nil {
		t.Fatalf("apply migration to target: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(migration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	return db, dir
}
