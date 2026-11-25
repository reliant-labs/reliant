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

// clinicMigration is a realistic 4-entity schema carrying EVERY constraint
// shape forge's own DDL projects from proto field rules, plus the UNIQUE
// non-PK column that a real domain schema always has (order_number / sku /
// slug / email):
//
//   - char_length cap        patients.date_of_birth  (max_len: 10)
//   - char_length min+max    visit_notes.author_initials
//   - declared varchar cap   patients.room_code
//   - enum IN (...)          patients.status, orders.status
//   - numeric range          clinics.capacity, orders.total_cents
//   - UNIQUE non-PK          clinics.name (built-in pool), orders.order_number
//
// Seeding this schema against a real postgres is the whole test: postgres
// enforces every one of those constraints, so an INSERT that lands IS the
// proof that synthesis respected them.
const clinicMigration = `
CREATE TABLE clinics (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    capacity INTEGER NOT NULL CHECK (capacity >= 1 AND capacity <= 50),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE patients (
    id TEXT PRIMARY KEY,
    clinic_id TEXT NOT NULL REFERENCES clinics(id),
    name TEXT NOT NULL,
    email TEXT NOT NULL,
    date_of_birth TEXT NOT NULL CHECK (char_length(date_of_birth) <= 10),
    room_code VARCHAR(4) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'inactive', 'archived'))
);

CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    patient_id TEXT NOT NULL REFERENCES patients(id),
    order_number TEXT NOT NULL UNIQUE,
    total_cents INTEGER NOT NULL CHECK (total_cents >= 0 AND total_cents <= 100000),
    status TEXT NOT NULL CHECK (status IN ('pending', 'paid', 'refunded'))
);

CREATE TABLE visit_notes (
    id TEXT PRIMARY KEY,
    patient_id TEXT NOT NULL REFERENCES patients(id),
    body TEXT NOT NULL,
    author_initials TEXT NOT NULL CHECK (char_length(author_initials) BETWEEN 2 AND 3)
);
`

// clinicVocab is the operator's overlay from the build run that surfaced these
// defects: a 24-value pool for a UNIQUE column at a 20-row target. Pool depth
// alone can never fix a with-replacement draw, so this is the shape that must
// seed distinct values.
const clinicVocab = `
pools:
  order_numbers:
    [ORD-1001, ORD-1002, ORD-1003, ORD-1004, ORD-1005, ORD-1006,
     ORD-1007, ORD-1008, ORD-1009, ORD-1010, ORD-1011, ORD-1012,
     ORD-1013, ORD-1014, ORD-1015, ORD-1016, ORD-1017, ORD-1018,
     ORD-1019, ORD-1020, ORD-1021, ORD-1022, ORD-1023, ORD-1024]
columns:
  orders.order_number: {pool: order_numbers}
`

// setupClinicDB creates a pgtest database, applies the clinic migration, and
// lays out a project-shaped tree (db/migrations + db/seeds) so the vocab
// overlay resolves the way it does in a real project.
func setupClinicDB(t *testing.T, vocab string) (*sql.DB, string) {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(clinicMigration); err != nil {
		t.Fatalf("apply clinic migration to target: %v", err)
	}
	base := t.TempDir()
	migDir := filepath.Join(base, "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migDir, "00001_init.up.sql"), []byte(clinicMigration), 0o644); err != nil {
		t.Fatal(err)
	}
	if vocab != "" {
		if err := os.MkdirAll(filepath.Join(base, "seeds"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(VocabPath(migDir), []byte(vocab), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return db, migDir
}

// A realistic schema carrying all four constraint shapes seeds ROWS — the
// regression test for the three defects a real build run hit:
//
//	BUG 1  char_length CHECK violated by synthesis (whole run rolled back)
//	BUG 2  UNIQUE non-PK column collided; pool depth could not fix it
//	BUG 3  the same values baked into pkg/app/factory_gen.go (see the
//	       Rows:1 factory-shaped assertion below)
//
// Every INSERT going in IS the proof: postgres rejects any value that misses
// a CHECK, a length cap, or a unique index.
func TestMaterialize_AllConstraintShapesOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, migDir := setupClinicDB(t, clinicVocab)

	const rows = 20
	res, err := Materialize(ctx, db, migDir, "", Config{Rows: rows, Salt: 1})
	if err != nil {
		t.Fatalf("Materialize (a CHECK/length/unique violation surfaces here): %v", err)
	}

	// Rows actually landed — not "the statement was valid", but data present.
	assertCount(t, db, "clinics", rows)
	assertCount(t, db, "patients", rows)
	assertCount(t, db, "orders", rows)
	assertCount(t, db, "visit_notes", rows)
	if got := res.Total(); got != 4*rows {
		t.Fatalf("total seeded rows = %d, want %d", got, 4*rows)
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
	// char_length cap: the constraint forge derives from `max_len: 10`.
	assertZero("patients.date_of_birth within the 10-char cap",
		`SELECT count(*) FROM patients WHERE char_length(date_of_birth) > 10`)
	// char_length min AND max on one column.
	assertZero("visit_notes.author_initials within [2,3] chars",
		`SELECT count(*) FROM visit_notes WHERE char_length(author_initials) NOT BETWEEN 2 AND 3`)
	// Declared varchar cap (no CHECK constraint at all).
	assertZero("patients.room_code within the varchar(4) cap",
		`SELECT count(*) FROM patients WHERE char_length(room_code) > 4`)
	// Enum vocabulary.
	assertZero("patients.status inside the CHECK vocabulary",
		`SELECT count(*) FROM patients WHERE status NOT IN ('active','inactive','archived')`)
	// Numeric range.
	assertZero("orders.total_cents inside [0,100000]",
		`SELECT count(*) FROM orders WHERE total_cents NOT BETWEEN 0 AND 100000`)

	// UNIQUE non-PK columns: distinct across every seeded row.
	assertDistinct(t, db, "orders", "order_number", rows)
	assertDistinct(t, db, "clinics", "name", rows)

	// The UNIQUE column still draws the author's vocabulary — uniqueness is
	// achieved by drawing WITHOUT replacement, not by mangling their values.
	assertZero("orders.order_number drawn from the author's vocab pool",
		`SELECT count(*) FROM orders WHERE order_number NOT LIKE 'ORD-10%'`)

	// Idempotent, as the seeder promises.
	res2, err := Materialize(ctx, db, migDir, "", Config{Rows: rows, Salt: 1})
	if err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
	if res2.Total() != 0 {
		t.Fatalf("second apply must be a no-op (ON CONFLICT), inserted %d", res2.Total())
	}
}

// A UNIQUE column with NO vocabulary overlay must still seed distinct values:
// clinics.name draws from the built-in company pool (20 entries) and
// orders.order_number from the generic placeholder shape.
func TestMaterialize_UniqueColumnsWithoutVocabOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, migDir := setupClinicDB(t, "")

	const rows = 20
	if _, err := Materialize(ctx, db, migDir, "", Config{Rows: rows, Salt: 3}); err != nil {
		t.Fatalf("Materialize without a vocab overlay: %v", err)
	}
	assertCount(t, db, "clinics", rows)
	assertCount(t, db, "orders", rows)
	assertDistinct(t, db, "clinics", "name", rows)
	assertDistinct(t, db, "orders", "order_number", rows)
}

// The typed entity factories (pkg/app/factory_gen.go) bake THIS planner's
// statements — a Rows:1 plan over each entity's FK closure. BUG 3 was the
// factory emitting `date_of_birth = "sample_date_of_birth_1"` (22 chars)
// against the 10-char CHECK, i.e. the seed planner's defect surfacing on a
// second, independent artifact. Executing the same Rows:1 statements against
// real postgres is the factory-shaped proof that the shared cause is fixed.
func TestFactoryShapedPlan_SatisfiesChecksOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, migDir := setupClinicDB(t, "")

	plan, err := BuildLivePlan(ctx, db, migDir, "", Config{Rows: 1, Salt: 0})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	for _, stmt := range plan.Statements() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("factory-shaped statement rejected by postgres: %v\n%s", err, stmt)
		}
	}
	assertCount(t, db, "patients", 1)

	// The exact value BUG 3 reported, read back from the row the factory SQL
	// would have inserted.
	var dob string
	if err := db.QueryRow(`SELECT date_of_birth FROM patients LIMIT 1`).Scan(&dob); err != nil {
		t.Fatal(err)
	}
	if len(dob) > 10 {
		t.Fatalf("date_of_birth = %q (%d chars) exceeds the 10-char CHECK", dob, len(dob))
	}
}

// A constraint that genuinely CANNOT be satisfied is reported at PLAN time,
// naming the table.column and the conflict — not as a postgres SQLSTATE from
// the middle of an INSERT, and never after a partial run.
const unsatisfiableMigration = `
CREATE TABLE badges (
    id TEXT PRIMARY KEY,
    grade TEXT NOT NULL UNIQUE CHECK (grade IN ('gold', 'silver'))
);
`

func TestBuildLivePlan_UnsatisfiableUniqueVocabularyCapsRows(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(unsatisfiableMigration); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(unsatisfiableMigration), 0o644); err != nil {
		t.Fatal(err)
	}

	// A UNIQUE column whose CHECK vocabulary holds 2 values can never carry
	// 20 rows. The plan caps the table at the vocabulary's size and says so,
	// rather than letting postgres abort the run mid-INSERT.
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	warns := strings.Join(plan.Warnings(), "\n")
	if !strings.Contains(warns, "badges.grade") {
		t.Errorf("expected a plan warning naming badges.grade, got:\n%s", warns)
	}
	res, err := Apply(ctx, db, plan)
	if err != nil {
		t.Fatalf("Apply on a capped table must succeed: %v", err)
	}
	if res.Total() != 2 {
		t.Fatalf("seeded %d row(s), want 2 (the vocabulary's size)", res.Total())
	}
	assertCount(t, db, "badges", 2)
}

func assertDistinct(t *testing.T, db *sql.DB, table, column string, want int) {
	t.Helper()
	var n int
	q := "SELECT count(DISTINCT " + quoteIdent(column) + ") FROM " + quoteIdent(table)
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("count distinct %s.%s: %v", table, column, err)
	}
	if n != want {
		t.Fatalf("%s.%s has %d distinct value(s), want %d", table, column, n, want)
	}
}
