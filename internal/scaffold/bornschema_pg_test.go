// File: internal/scaffold/bornschema_pg_test.go
//
// The one property that subsumes a whole class of birth defects:
//
//	FORGE'S GENERATED DATA MUST SATISFY FORGE'S GENERATED SCHEMA.
//
// forge emits both halves of a new entity — the CREATE TABLE (this
// package) and the rows that fill it (pkg/seedplan, via `forge db
// seed` and `forge env up`'s auto-seed). Nothing checked that the two agree.
// A measured real-workflow run spent roughly a third of its scaffold
// budget repairing forge's own output before it could start work: born
// fixtures that violated the CHECK constraint sitting in the migration
// beside them, and an auto-seed that died outright because it generates
// every column independently and so cannot satisfy a constraint that
// spans two of them.
//
// Grepping the emitted SQL would not have caught any of it — every
// individual statement was well-formed. Only BUILDING it does: render the
// migrations, apply them to a real postgres, run the real seeder against
// the applied schema, and require every planned row to land. A row that
// violates its own table's constraints cannot survive that, whatever
// spelling the violation takes (CHECK, UNIQUE, NOT NULL, FK), and neither
// can a future regression in a shape nobody has thought of yet.
//
// The corpus is the shapes that actually broke, kept deliberately small
// and generic — an optional foreign key, a two-column ordering CHECK, a
// UNIQUE column, a date-only column, an enum with a proto zero sentinel,
// a bool, a numeric bound. It is a SHAPE corpus, not a domain: a project
// with one entity, forty entities, or no frontend at all exercises the
// same emitter through the same path.
//
// The migrations are BUILT, never hand-written: hand-written SQL would
// test the fixture instead of forge. What the corpus does add by hand is
// the `authored` half — the constraints a real owner adds AFTER birth. The
// birth migration is user-owned from its first byte (see the file header
// of entityproto.go), and forge's own suggestion machinery tells authors to
// add indexes and constraints in migrations they own. The seeder therefore
// has to satisfy the APPLIED schema, not just the part forge typed.

package scaffold

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

const bornPkg = "services.clinic.v1"

// bornEntity is one corpus member: the proto-side spec forge births a
// migration from, plus the SQL its owner adds afterwards.
type bornEntity struct {
	// shapes names the constraint shapes this entity contributes, so a
	// failure report says which shape regressed rather than which domain
	// noun happened to carry it.
	shapes string
	spec   EntityFromProtoSpec
	// authored is SQL appended to the birth migration by the migration's
	// OWNER — the constraints forge itself has no vocabulary to declare
	// (UNIQUE, a two-column CHECK, a DATE column). It is part of the
	// applied schema, so the seeder must satisfy it.
	authored string
}

// bornCorpus is the shape corpus. Order is birth order: a reference can
// only be constrained once the referenced table exists, and the renderer
// splits its foreign keys on exactly that (see planForeignKeys).
func bornCorpus() []bornEntity {
	u := func(n uint64) *uint64 { return &n }
	return []bornEntity{
		{
			shapes: "UNIQUE data column, date-only column, bool, length CHECK, email CHECK, soft delete",
			spec: EntityFromProtoSpec{
				Table:     "patients",
				MessageFQ: bornPkg + ".Patient",
				ProtoPkg:  bornPkg,
				Fields: []codegen.SchemaFieldDef{
					{Name: "id", Kind: "string"},
					{Name: "full_name", Kind: "string", Validate: &codegen.FieldConstraints{MinLen: u(2), MaxLen: u(120)}},
					{Name: "email", Kind: "string", Validate: &codegen.FieldConstraints{Email: true}},
					// A calendar date. forge has no way to declare one, so it
					// is born TIMESTAMPTZ — see
					// TestRenderEntityMigrationFromProto_UniqueAndDateAreNotDeclarable.
					{Name: "date_of_birth", Kind: "message", TypeName: "google.protobuf.Timestamp"},
					{Name: "consent_on_file", Kind: "bool"},
				},
				Timestamps: true,
				SoftDelete: true,
			},
			authored: "ALTER TABLE patients ADD CONSTRAINT patients_email_key UNIQUE (email);\n" +
				"ALTER TABLE patients ADD COLUMN enrolled_on DATE NOT NULL;\n",
		},
		{
			shapes: "NOT NULL foreign key, enum with a proto zero sentinel, numeric range CHECK, two-column ordering CHECK",
			spec: EntityFromProtoSpec{
				Table:     "prescriptions",
				MessageFQ: bornPkg + ".Prescription",
				ProtoPkg:  bornPkg,
				Fields: []codegen.SchemaFieldDef{
					{Name: "id", Kind: "string"},
					{Name: "patient_id", Kind: "string"},
					{Name: "status", Kind: "enum", TypeName: bornPkg + ".PrescriptionStatus"},
					{Name: "issued_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
					{Name: "expires_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
					{Name: "refills_remaining", Kind: "int32", Validate: &codegen.FieldConstraints{Gte: "0", Lte: "11"}},
				},
				Enums: map[string][]string{
					bornPkg + ".PrescriptionStatus": {
						"PRESCRIPTION_STATUS_UNSPECIFIED",
						"PRESCRIPTION_STATUS_ACTIVE",
						"PRESCRIPTION_STATUS_EXPIRED",
					},
				},
				Timestamps: true,
			},
			// The validity window: the constraint shape that spans two
			// columns, which every per-column constraint model in the seeder
			// is structurally blind to.
			authored: "ALTER TABLE prescriptions ADD CONSTRAINT prescriptions_validity_window CHECK (expires_at > issued_at);\n",
		},
		{
			shapes: "OPTIONAL foreign key (must be nullable), bool safety flag, lower-bounded integer",
			spec: EntityFromProtoSpec{
				Table:     "orders",
				MessageFQ: bornPkg + ".Order",
				ProtoPkg:  bornPkg,
				Fields: []codegen.SchemaFieldDef{
					{Name: "id", Kind: "string"},
					{Name: "patient_id", Kind: "string"},
					// The exact shape that cost a measured run: an OPTIONAL
					// relationship. A NOT NULL column here forces every order
					// to carry a prescription, contradicting the model.
					{Name: "prescription_id", Kind: "string", Optional: true},
					{Name: "rx_required", Kind: "bool"},
					{Name: "total_cents", Kind: "int64", Validate: &codegen.FieldConstraints{Gte: "0"}},
					{Name: "note", Kind: "string", Optional: true},
				},
				Timestamps: true,
			},
			// An order reaches patients two ways — directly, and through its
			// prescription — so the seeder refuses until someone says which
			// path is authoritative (see seeddata/diamond.go). The domain
			// answer: an order's patient IS its prescription's patient.
			authored: "COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS 'forge:ref derived-from=prescription_id';\n",
		},
		{
			shapes: "NULL-GUARDED two-column ordering CHECK, guarded non-strict integer pair",
			spec: EntityFromProtoSpec{
				Table:     "appointments",
				MessageFQ: bornPkg + ".Appointment",
				ProtoPkg:  bornPkg,
				Fields: []codegen.SchemaFieldDef{
					{Name: "id", Kind: "string"},
					{Name: "patient_id", Kind: "string"},
					// forge births every Timestamp column NULLABLE (see
					// entityproto.go), so the constraint an owner writes over
					// a pair of them is guarded — a row that has not happened
					// yet has no window to order. The guard does not change
					// the ordering requirement, only which rows it binds.
					{Name: "scheduled_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
					{Name: "completed_at", Kind: "message", TypeName: "google.protobuf.Timestamp"},
					{Name: "min_duration_minutes", Kind: "int32", Validate: &codegen.FieldConstraints{Gte: "5", Lte: "60"}},
					{Name: "max_duration_minutes", Kind: "int32", Validate: &codegen.FieldConstraints{Gte: "5"}},
				},
				Timestamps: true,
			},
			authored: "ALTER TABLE appointments ADD CONSTRAINT appointments_completed_after_scheduled " +
				"CHECK (scheduled_at IS NULL OR completed_at IS NULL OR completed_at > scheduled_at);\n" +
				"ALTER TABLE appointments ADD CONSTRAINT appointments_duration_range " +
				"CHECK (min_duration_minutes IS NULL OR max_duration_minutes IS NULL OR max_duration_minutes >= min_duration_minutes);\n",
		},
	}
}

// TestBornSchemaAndSeedAgree is the property: every row forge's seeder
// plans for a forge-born schema is accepted by that schema on real
// postgres. Postgres enforcing its own constraints IS the proof — no
// assertion here can be fooled by a well-formed statement that happens to
// be wrong.
//
// It is also proof against the OTHER false green: a seeder that satisfies
// a hard table by seeding nothing into it. Every table's live row count is
// checked against the configured target, so a silently emptied table fails
// exactly as loudly as a rejected row.
func TestBornSchemaAndSeedAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	ctx := context.Background()
	// Enough rows that the optional relationship is actually declined by
	// some of them (the seeder nulls an optional foreign key on roughly one
	// row in five) and that a UNIQUE column has to produce real variety.
	const rows = 12

	db, migDir := applyBornCorpus(t)
	if _, err := schemadef.ApplyAndIntrospect(migDir); err != nil {
		t.Fatalf("the born migrations do not survive the shadow apply forge generate runs: %v", err)
	}

	cfg := seedplan.Config{Rows: rows, Salt: 1}
	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, "", cfg)
	if err != nil {
		t.Fatalf("BuildLivePlan over the born schema: %v", err)
	}
	if warns := plan.Warnings(); len(warns) > 0 {
		// A warning here means the plan quietly shrank a table or refitted a
		// value. That is exactly the "seeded nothing, reported success" shape,
		// so the corpus treats it as a failure and names it.
		t.Errorf("the seed plan for a forge-born schema must need no compromises; got:\n  %s",
			strings.Join(warns, "\n  "))
	}

	// Apply is the assertion. Every constraint in the applied schema —
	// CHECK, UNIQUE, NOT NULL, foreign key — is enforced by postgres on
	// every row, so a fixture that contradicts the migration it was born
	// beside cannot survive this call. seedplan.Apply names the constraint
	// and prints the offending planned values (see seeddata/explain.go).
	if _, err := seedplan.Apply(ctx, db, plan); err != nil {
		t.Fatalf("%v", err)
	}

	// Every corpus table carries its full row target — no table silently
	// emptied, none capped.
	for _, e := range bornCorpus() {
		var n int
		if qerr := db.QueryRow("SELECT count(*) FROM " + e.spec.Table).Scan(&n); qerr != nil {
			t.Fatalf("count %s: %v", e.spec.Table, qerr)
		}
		if n != rows {
			t.Errorf("%s holds %d row(s), want %d — shapes: %s", e.spec.Table, n, rows, e.shapes)
		}
	}

	// The two-column CHECK is live for every row, not vacuously true on a
	// NULL: assert the window is real data, so the constraint above is a
	// constraint and not decoration.
	var open int
	if err := db.QueryRow(
		`SELECT count(*) FROM prescriptions WHERE issued_at IS NULL OR expires_at IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%d prescription row(s) leave the validity window NULL — the two-column CHECK passes vacuously there", open)
	}

	// The NULL-GUARDED window likewise. This one needs the assertion more
	// than the bare form does: a guard is satisfied by a NULL, so a seeder
	// that gave up on the constraint and nulled both ends would insert every
	// row and prove nothing. Rows landing IS the test only if the rows are
	// inside the part of the constraint that binds.
	var vacuous int
	if err := db.QueryRow(
		`SELECT count(*) FROM appointments WHERE scheduled_at IS NULL OR completed_at IS NULL`).Scan(&vacuous); err != nil {
		t.Fatal(err)
	}
	if vacuous != 0 {
		t.Errorf("%d appointment row(s) leave the guarded window NULL — the CHECK passes vacuously there and orders nothing", vacuous)
	}

	// The optional relationship really is optional in the DATA, not just in
	// the DDL: at least one order declines the prescription. (A schema that
	// permits NULL but data that never uses it would leave the nullable path
	// untested downstream.)
	var declined int
	if err := db.QueryRow(`SELECT count(*) FROM orders WHERE prescription_id IS NULL`).Scan(&declined); err != nil {
		t.Fatal(err)
	}
	if declined == 0 {
		t.Errorf("no seeded order declines the OPTIONAL prescription relationship; the nullable path is never exercised")
	}
}

// TestBornSchemaSeedIsIdempotentAndResettable pins the two operations
// `forge env up` actually performs against a born schema: auto-seed on an
// empty database, and `forge db seed --reset`. Both must round-trip on the
// SAME constraints — a reset that truncates and re-inserts re-runs every
// constraint the first apply did.
func TestBornSchemaSeedIsIdempotentAndResettable(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	ctx := context.Background()
	db, migDir := applyBornCorpus(t)
	cfg := seedplan.Config{Rows: 4, Salt: 3}

	plan, err := seedplan.BuildLivePlan(ctx, db, migDir, "", cfg)
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	first, err := seedplan.Apply(ctx, db, plan)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if first.Total() != int64(4*len(bornCorpus())) {
		t.Fatalf("first apply inserted %d rows, want %d", first.Total(), 4*len(bornCorpus()))
	}
	again, err := seedplan.Apply(ctx, db, plan)
	if err != nil {
		t.Fatalf("re-applying a seeded plan must be a no-op: %v", err)
	}
	if again.Total() != 0 {
		t.Errorf("second apply inserted %d rows, want 0 (ON CONFLICT DO NOTHING)", again.Total())
	}
	if _, err := seedplan.Reset(ctx, db, plan); err != nil {
		t.Fatalf("reset re-runs every constraint: %v", err)
	}
}

// applyBornCorpus renders every corpus entity's birth migration, applies
// the whole set to a fresh real postgres, and writes the same files into a
// migrations dir the seeder introspects. Returns the live database and the
// migrations directory.
func applyBornCorpus(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)

	migDir := filepath.Join(t.TempDir(), "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}

	corpus := bornCorpus()
	known := map[string]bool{}
	for _, e := range corpus {
		known[e.spec.Table] = true
	}
	var existing []ExistingTable

	for i, e := range corpus {
		spec := e.spec
		spec.KnownTables = known
		spec.ExistingTables = existing
		mig := RenderEntityMigrationFromProto(spec)
		up := mig.UpSQL + e.authored

		base := fmt.Sprintf("%05d_create_%s", i+1, spec.Table)
		write := func(name, body string) {
			if werr := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0o644); werr != nil {
				t.Fatal(werr)
			}
		}
		write(base+".up.sql", up)

		// Apply strictly. Every statement forge emitted must run; a
		// best-effort apply would let a broken constraint vanish and the
		// seed then "pass" against a schema that was never installed.
		for _, stmt := range schemadef.SplitStatements(up) {
			if strings.TrimSpace(stripSQLComments(stmt)) == "" {
				continue
			}
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s (%s): birth migration statement failed to apply:\n%s\nerr: %v",
					spec.Table, e.shapes, strings.TrimSpace(stmt), err)
			}
		}

		existing = append(existing, ExistingTable{Name: spec.Table, UnconstrainedRefColumns: mig.PendingRefColumns})
		for child, cols := range mig.BackfilledRefColumns {
			for i := range existing {
				if existing[i].Name != child {
					continue
				}
				existing[i].UnconstrainedRefColumns = removeAll(existing[i].UnconstrainedRefColumns, cols)
			}
		}
	}
	return db, migDir
}

func removeAll(from, drop []string) []string {
	gone := map[string]bool{}
	for _, d := range drop {
		gone[d] = true
	}
	var out []string
	for _, v := range from {
		if !gone[v] {
			out = append(out, v)
		}
	}
	return out
}
