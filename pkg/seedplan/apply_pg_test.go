package seedplan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

func requirePG(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
}

const fkMigration = `
CREATE TYPE appt_kind AS ENUM ('checkup', 'followup', 'intake');

CREATE TABLE patients (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    name TEXT NOT NULL,
    email TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    tags TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE appointments (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    patient_id TEXT NOT NULL REFERENCES patients(id),
    kind appt_kind NOT NULL DEFAULT 'checkup',
    reason TEXT NOT NULL,
    prior_id TEXT REFERENCES appointments(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// setupFKDB creates a pgtest database, applies the FK migration to it, and
// writes the same migration into a temp migrations dir (for ApplyAndIntrospect
// to model). Returns the db, the migrations dir, and registers cleanup.
func setupFKDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(fkMigration); err != nil {
		t.Fatalf("apply migration to target: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(fkMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	return db, dir
}

// The seeded rows satisfy every foreign key on a real postgres — the INSERTs
// succeeding IS the FK-coherence proof (postgres enforces the constraints).
// It also proves enum/CHECK pools are respected (an unconstrained value would
// abort the INSERT) and that self-referential + array + soft-delete columns
// materialize cleanly.
func TestMaterialize_FKCoherentOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)

	res, err := Materialize(ctx, db, dir, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("Materialize (FK/enum/CHECK violation would surface here): %v", err)
	}
	if res.Total() != 40 {
		t.Fatalf("expected 40 rows (20 patients + 20 appointments), got %d", res.Total())
	}

	assertCount(t, db, "patients", 20)
	assertCount(t, db, "appointments", 20)

	// Idempotent: a second apply inserts nothing.
	res2, err := Materialize(ctx, db, dir, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
	if res2.Total() != 0 {
		t.Fatalf("second apply must be a no-op (ON CONFLICT), inserted %d", res2.Total())
	}
}

// oneToOneMigration: intakes.order_id is a NOT NULL UNIQUE foreign key to
// orders — a 1-1 relationship (the dogfood shape that surfaced the defect).
const oneToOneMigration = `
CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    total_cents INTEGER NOT NULL
);

CREATE TABLE intakes (
    id TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    order_id TEXT NOT NULL UNIQUE REFERENCES orders(id),
    notes TEXT NOT NULL
);
`

// A UNIQUE foreign key (1-1 relationship) seeds distinct parents. Pre-fix the
// deterministic seeder hash-picked parent rows and collided two intakes onto
// one order, so the INSERT aborted with `duplicate key value violates unique
// constraint "intakes_order_id_key"`. Postgres enforcing the constraint IS the
// proof: the INSERT succeeding means every order_id is distinct.
func TestMaterialize_UniqueFK_OneToOneOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(oneToOneMigration); err != nil {
		t.Fatalf("apply migration to target: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(oneToOneMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	res, err := Materialize(ctx, db, dir, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("Materialize (a duplicate UNIQUE FK value would abort here): %v", err)
	}
	// 20 orders + 20 intakes (child count equals parent count here).
	assertCount(t, db, "orders", 20)
	assertCount(t, db, "intakes", 20)
	if res.Total() != 40 {
		t.Fatalf("expected 40 rows, got %d", res.Total())
	}

	// Every intake references a distinct order (the unique constraint already
	// guaranteed it; assert the 1-1 shape explicitly).
	var distinct int
	if err := db.QueryRow(`SELECT count(DISTINCT order_id) FROM intakes`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 20 {
		t.Fatalf("intakes reference %d distinct orders, want 20 (1-1)", distinct)
	}
}

// A 1-1 child whose parent has fewer rows caps at the parent's count — the
// INSERT still satisfies the unique + FK constraints on real postgres.
func TestMaterialize_UniqueFK_CapsChildAtParentOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(oneToOneMigration); err != nil {
		t.Fatalf("apply migration to target: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(oneToOneMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	// Fewer orders than the default row count: intakes must cap at 3.
	cfg := Config{Rows: 20, Salt: 1, RowsPerTable: map[string]int{"orders": 3}}
	if _, err := Materialize(ctx, db, dir, "", cfg); err != nil {
		t.Fatalf("Materialize with capped 1-1 child: %v", err)
	}
	assertCount(t, db, "orders", 3)
	assertCount(t, db, "intakes", 3)
}

// IntrospectEnumPools reads both native enum labels and simple CHECK (col IN
// (...)) pools from the live database.
func TestIntrospectEnumPools(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, _ := setupFKDB(t)

	pools, err := IntrospectEnumPools(ctx, db)
	if err != nil {
		t.Fatalf("IntrospectEnumPools: %v", err)
	}
	if got, ok := pools.get("patients", "status"); !ok || !sameSet(got, []string{"active", "archived"}) {
		t.Fatalf("patients.status CHECK pool = %v (ok=%v), want [active archived]", got, ok)
	}
	if got, ok := pools.get("appointments", "kind"); !ok || !sameSet(got, []string{"checkup", "followup", "intake"}) {
		t.Fatalf("appointments.kind enum pool = %v (ok=%v), want [checkup followup intake]", got, ok)
	}
}

// Reset wipes and re-seeds child-first without violating FKs.
func TestReset_RoundTrips(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 10}); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Reset(ctx, db, plan); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	assertCount(t, db, "patients", 10)
	assertCount(t, db, "appointments", 10)
}

// vocabMigration exercises the vocab overlay against real constraints: a
// CHECK vocabulary, a char_length cap, an FK parent, and a table whose
// columns nothing in the schema describes.
const vocabMigration = `
CREATE TABLE brands (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE products (
    id TEXT PRIMARY KEY,
    brand_id TEXT NOT NULL REFERENCES brands(id),
    name TEXT NOT NULL,
    currency TEXT NOT NULL CHECK (char_length(currency) = 3),
    status TEXT NOT NULL CHECK (status IN ('active', 'archived'))
);

CREATE TABLE patients (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL
);
`

const vocabOverlay = `
pools:
  peptide_names: [BPC-157, Semaglutide, CJC-1295]
columns:
  products.name: {pool: peptide_names}
  brands.name: [VitalPep, PepCore Labs]
  products.status: [active, bogus]
  products.currency: [DOLLARS]
`

// The db/seeds/vocab.yaml overlay drives real INSERTs: vocab names land in
// the rows, constraint-invalid values are skipped with warnings (the INSERT
// succeeding proves the survivors satisfy the real constraints), and a
// fully-invalid column falls back to synthesis — which, for a column nothing
// in the schema describes, means the emitter's placeholder fitted to the
// column's declared length. This is the end-to-end proof that vocab.yaml is
// the replacement for everything the seeder no longer guesses.
func TestMaterialize_VocabOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(vocabMigration); err != nil {
		t.Fatalf("apply migration to target: %v", err)
	}
	base := t.TempDir()
	migDir := filepath.Join(base, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(vocabMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "seeds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(migDir), []byte(vocabOverlay), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildLivePlan(ctx, db, migDir, "", Config{Rows: 10, Salt: 1})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	wantWarns := []string{
		`products.status: value "bogus"`,            // not in the CHECK vocabulary
		`products.currency: value "DOLLARS"`,        // exceeds char_length(currency)=3
		"products.currency: no valid values remain", // → built-in USD
	}
	for _, want := range wantWarns {
		found := false
		for _, w := range plan.VocabWarnings() {
			if strings.Contains(w, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing warning %q in:\n%s", want, strings.Join(plan.VocabWarnings(), "\n"))
		}
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply (a constraint-violating vocab value would abort here): %v", err)
	}

	assertZero := func(desc, query string) {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d row(s) violate: %s", n, desc)
		}
	}
	assertZero("products.name from the peptide vocab pool",
		`SELECT count(*) FROM products WHERE name NOT IN ('BPC-157', 'Semaglutide', 'CJC-1295')`)
	assertZero("brands.name from the inline vocab list",
		`SELECT count(*) FROM brands WHERE name NOT IN ('VitalPep', 'PepCore Labs')`)
	assertZero("products.status only the valid vocab value",
		`SELECT count(*) FROM products WHERE status <> 'active'`)
	// The whole vocabulary was rejected, so the column falls back to synthesis.
	// The INSERT succeeding already proves the fallback satisfies
	// char_length(currency) = 3; what this pins is that forge did not decide
	// WHICH currency the app trades in. `USD` used to be the answer for any
	// column spelled currency / *_currency, at every row.
	assertZero("products.currency is not an ISO-4217 code forge invented",
		`SELECT count(*) FROM products WHERE currency = 'USD'`)
	assertZero("patients.name is a synthetic placeholder — nothing declares what it holds",
		`SELECT count(*) FROM patients WHERE name NOT LIKE '`+SyntheticStringPrefix+`%'`)
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if n != want {
		t.Fatalf("%s row count = %d, want %d", table, n, want)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, v := range a {
		m[v] = true
	}
	for _, v := range b {
		if !m[v] {
			return false
		}
	}
	return true
}

// pinMigration is a plain identity-and-ownership schema: people, and rows that
// belong to one of them. Both keys are `uuid`, which is what makes the shape of
// a key a real constraint rather than a convention.
const pinMigration = `
CREATE TABLE users (
    id UUID PRIMARY KEY,
    external_id UUID NOT NULL,
    email TEXT NOT NULL,
    name TEXT NOT NULL
);

CREATE TABLE notes (
    id UUID PRIMARY KEY,
    owner_id UUID NOT NULL REFERENCES users(id),
    body TEXT NOT NULL
);
`

// pinOverlay asks db/seeds/vocab.yaml for four pins. Two are the seeder's to
// give (a plain column, and a uuid-typed one whose shape it validates); two are
// keys, which it never hands over.
const pinOverlay = `
columns:
  users.email: ["known@example.com"]
  users.external_id: ["3f2504e0-4f89-41d3-9a0c-0305e82c3301", "not-a-uuid"]
  users.id: ["11111111-1111-4111-8111-111111111111"]
  notes.owner_id: ["11111111-1111-4111-8111-111111111111"]
`

// What an app can pin, and what it cannot. The overlay is where a project
// states a value forge could not have known — including on a `uuid` column,
// whose shape is validated before the transactional seed can be aborted by it.
// KEYS are not on offer: primary and foreign keys are the seeder's referential
// machinery, so an app cannot declare which id its dev principal carries, and
// cannot point a child row at a row it names. What it gets instead is the row-0
// spine — every table's row 0 references its parents' row 0 — and SeedValue to
// read the id back out.
func TestMaterialize_VocabPinsAValueButNeverAKeyOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(pinMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	base := t.TempDir()
	migDir := filepath.Join(base, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(pinMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "seeds"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(migDir), []byte(pinOverlay), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildLivePlan(ctx, db, migDir, "", Config{Rows: 8, Salt: 5})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	wantWarns := []string{
		"users.id: primary-key column",
		"notes.owner_id: foreign-key column",
		`users.external_id: value "not-a-uuid" is not a UUID`,
	}
	for _, want := range wantWarns {
		found := false
		for _, w := range plan.Warnings() {
			if strings.Contains(w, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing warning %q in:\n%s", want, strings.Join(plan.Warnings(), "\n"))
		}
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	assertNone := func(desc, query string) {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d row(s) violate: %s", n, desc)
		}
	}
	assertNone("every users.email is the pinned literal",
		`SELECT count(*) FROM users WHERE email <> 'known@example.com'`)
	assertNone("every users.external_id is the pinned UUID (the malformed one was skipped)",
		`SELECT count(*) FROM users WHERE external_id <> '3f2504e0-4f89-41d3-9a0c-0305e82c3301'`)
	assertNone("no users.id took the pinned literal — the key stayed the seeder's",
		`SELECT count(*) FROM users WHERE id = '11111111-1111-4111-8111-111111111111'`)

	// The spine is what an app gets instead of a declarable key: notes row 0
	// belongs to users row 0, and SeedValue is how the id is read back.
	ownerID, ok := plan.SeedValue("users", "id", 0)
	if !ok || ownerID == "" {
		t.Fatal("SeedValue(users.id, row 0) returned nothing — the spine cannot be read back")
	}
	noteOwner, ok := plan.SeedValue("notes", "owner_id", 0)
	if !ok {
		t.Fatal("SeedValue(notes.owner_id, row 0) returned nothing")
	}
	if noteOwner != ownerID {
		t.Errorf("notes row 0 owner_id = %q, want users row 0 id %q", noteOwner, ownerID)
	}
	var got string
	if err := db.QueryRow(`SELECT id::text FROM users WHERE id = $1`, ownerID).Scan(&got); err != nil {
		t.Fatalf("users row 0 (%s) is not in the database: %v", ownerID, err)
	}
}
