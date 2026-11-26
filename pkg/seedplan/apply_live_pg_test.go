package seedplan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

func writeMigration(t *testing.T, dir, name, sql string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Status reports live-vs-planned counts per table, and AllSeedableTablesEmpty
// is the first-boot signal derived from the same counts.
func TestStatusAndEmptiness_FollowTheLiveCounts(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 6, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}

	empty, err := AllSeedableTablesEmpty(ctx, db, plan)
	if err != nil || !empty {
		t.Fatalf("a freshly migrated database is empty: %v, %v", empty, err)
	}
	st, err := Status(ctx, db, plan)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]TableStatus{"patients": {Table: "patients", Count: 0, Expected: 6}, "appointments": {Table: "appointments", Count: 0, Expected: 6}}
	if len(st) != len(want) {
		t.Fatalf("status = %+v", st)
	}
	for _, s := range st {
		if s != want[s.Table] {
			t.Errorf("before seeding, %s = %+v, want %+v", s.Table, s, want[s.Table])
		}
	}

	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatal(err)
	}
	if empty, err := AllSeedableTablesEmpty(ctx, db, plan); err != nil || empty {
		t.Errorf("a seeded database is not empty: %v, %v", empty, err)
	}
	st, _ = Status(ctx, db, plan)
	for _, s := range st {
		if s.Count != 6 || s.Expected != 6 {
			t.Errorf("after seeding, %s = %+v", s.Table, s)
		}
	}

	// A table the plan names but the database lost surfaces as an error naming it
	// (patients is emptied first: the emptiness check stops at the first populated table).
	if _, err := db.Exec("TRUNCATE patients CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE appointments"); err != nil {
		t.Fatal(err)
	}
	if _, err := Status(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "count ") {
		t.Errorf("Status over a missing table: %v", err)
	}
	if _, err := AllSeedableTablesEmpty(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "count ") {
		t.Errorf("AllSeedableTablesEmpty over a missing table: %v", err)
	}
}

func TestAllSeedableTablesEmpty_StopsAtTheFirstPopulatedTable(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 3, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO patients (id, region, name, email) VALUES ('p1', 'r', 'n', 'e')`); err != nil {
		t.Fatal(err)
	}
	if empty, err := AllSeedableTablesEmpty(ctx, db, plan); err != nil || empty {
		t.Errorf("one hand-made row makes the database populated: %v, %v", empty, err)
	}
}

func TestReset_RefusesAPlanThatCannotBeAppliedBeforeWipingAnything(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	if _, err := Materialize(ctx, db, dir, "", Config{Rows: 4, Salt: 1}); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 4, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	plan.vocabErrs = append(plan.vocabErrs, "patients.name: a null on a NOT NULL column")
	if _, err := Reset(ctx, db, plan); err == nil {
		t.Fatal("a plan that fails validation must be refused")
	}
	assertCount(t, db, "patients", 4)
	assertCount(t, db, "appointments", 4)
}

func TestReset_OfAnEmptyPlanIsAnApply(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, _ := setupFKDB(t)
	res, err := Reset(ctx, db, emptyPlan(Config{}))
	if err != nil || res.Total() != 0 || len(res.Tables) != 0 {
		t.Errorf("res = %+v, err = %v", res, err)
	}
}

func TestReset_ReportsATruncateFailure(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 2, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE appointments, patients"); err != nil {
		t.Fatal(err)
	}
	if _, err := Reset(ctx, db, plan); err == nil || !strings.Contains(err.Error(), "reset truncate") {
		t.Errorf("err = %v, want a reset-truncate failure", err)
	}
}

func TestApply_ExplainsAFailingInsertAndRollsBackTheRest(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 3, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The second table's rows reference parents; take the parents' table away
	// under the plan and the child insert fails after the parent one ran.
	if _, err := db.Exec("ALTER TABLE appointments RENAME TO appointments_gone"); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ctx, db, plan)
	if err == nil || !strings.Contains(err.Error(), "seed appointments") {
		t.Fatalf("err = %v, want a failure naming the table", err)
	}
	assertCount(t, db, "patients", 0) // one transaction: the parents' insert was undone
}

func TestApply_ReportsASequenceAdvanceFailure(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupSerialDB(t)
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 3, Salt: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Seeding writes the serial keys, then advances their sequence. Drop the
	// sequence's privileges' worth of existence: replace it so setval fails.
	if _, err := db.Exec(`ALTER TABLE tickets ALTER COLUMN id DROP DEFAULT; DROP SEQUENCE tickets_id_seq;
CREATE SEQUENCE tickets_id_seq MAXVALUE 2 START 1; ALTER TABLE tickets ALTER COLUMN id SET DEFAULT nextval('tickets_id_seq')`); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(ctx, db, plan)
	if err != nil && !strings.Contains(err.Error(), "advance tickets's sequences past the seeded keys") {
		t.Fatalf("err = %v", err)
	}
	if err == nil && res.Total() != 3 {
		t.Errorf("res = %+v", res)
	}
}

func setupSerialDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	const mig = `CREATE TABLE tickets (id SERIAL PRIMARY KEY, title TEXT NOT NULL);`
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(mig); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeMigration(t, dir, "00001_init.up.sql", mig)
	return db, dir
}

func TestMaterialize_ReportsWhatItCouldNotIntrospectOrPlan(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, _ := setupFKDB(t)

	// A directory that does not exist is a schema with no tables: nothing to seed, not a failure.
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if res, err := Materialize(ctx, db, missing, "", Config{Rows: 2}); err != nil || res.Total() != 0 {
		t.Errorf("a missing migrations directory seeds nothing: %+v, %v", res, err)
	}
	bad := t.TempDir()
	writeMigration(t, bad, "00001_init.up.sql", fkMigration)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(VocabPath(bad)), ""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(bad), []byte("columns: [not, a, mapping"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Materialize(ctx, db, bad, "", Config{Rows: 2}); err == nil {
		t.Error("a malformed vocab file is a hard error, not a silently skipped overlay")
	}
}

func TestBuildLivePlan_ScopedToNothingDoesNotReadTheOverlay(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, dir := setupFKDB(t)
	// A vocab file that would fail to load is never read when no table is in scope.
	if err := os.MkdirAll(filepath.Dir(VocabPath(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(VocabPath(dir), []byte("columns: [not, a, mapping"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 2, Tables: []string{"no_such_table"}})
	if err != nil {
		t.Fatalf("an empty scope has nothing for an overlay to describe: %v", err)
	}
	if len(plan.Tables()) != 0 {
		t.Errorf("tables = %v", plan.Tables())
	}
	res, err := Apply(ctx, db, plan)
	if err != nil || res.Total() != 0 {
		t.Errorf("applying an empty plan: %+v, %v", res, err)
	}
}

func TestBuildPlanFromDB_ReadsTheLiveSchemaWithNoMigrationsDirectory(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, _ := setupFKDB(t)
	plan, err := BuildPlanFromDB(ctx, db, Config{Rows: 5, Salt: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Tables(); !sameSet(got, []string{"patients", "appointments"}) {
		t.Fatalf("tables = %v", got)
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("a plan built from the live schema applies to it: %v", err)
	}
	assertCount(t, db, "appointments", 5)

	// Scoped to a leaf, the plan reaches its mandatory parents and nothing else.
	scoped, err := BuildPlanFromDB(ctx, db, Config{Rows: 2, Tables: []string{"appointments"}})
	if err != nil || !sameSet(scoped.Tables(), []string{"patients", "appointments"}) {
		t.Errorf("scoped tables = %v, %v", scoped.Tables(), err)
	}
	none, err := BuildPlanFromDB(ctx, db, Config{Rows: 2, Tables: []string{"ghost"}})
	if err != nil || len(none.Tables()) != 0 {
		t.Errorf("an unknown scope root plans nothing: %v, %v", none.Tables(), err)
	}

	closed, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := BuildPlanFromDB(ctx, closed, Config{Rows: 1}); err == nil || !strings.Contains(err.Error(), "introspect live schema") {
		t.Errorf("a closed database: %v", err)
	}
}

func TestMigrationsPending_NamesWhatBlocksSeeding(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	dir := t.TempDir()

	if b, err := MigrationsPending(ctx, db, filepath.Join(dir, "absent")); err != nil || b != nil {
		t.Errorf("no migrations on disk, nothing to be behind: %+v, %v", b, err)
	}
	if b, err := MigrationsPending(ctx, db, dir); err != nil || b != nil {
		t.Errorf("an empty directory: %+v, %v", b, err)
	}
	writeMigration(t, dir, "00002_two.up.sql", "SELECT 1;")
	writeMigration(t, dir, "00010_ten.up.sql", "SELECT 1;")
	writeMigration(t, dir, "00010_ten.down.sql", "SELECT 1;")
	writeMigration(t, dir, "notes.txt", "x")

	b, err := MigrationsPending(ctx, db, dir)
	if err != nil || b == nil || b.Dirty || b.Latest != "00010" || !strings.Contains(b.Reason, "no migrations applied") {
		t.Fatalf("no schema_migrations yet: %+v, %v", b, err)
	}

	set := func(version string, dirty bool) {
		t.Helper()
		if _, err := db.Exec(`DROP TABLE IF EXISTS schema_migrations; CREATE TABLE schema_migrations (version TEXT, dirty BOOLEAN)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations VALUES ($1, $2)`, version, dirty); err != nil {
			t.Fatal(err)
		}
	}
	set("00002", false)
	b, err = MigrationsPending(ctx, db, dir)
	if err != nil || b == nil || b.Dirty || b.Version != "00002" || b.Latest != "00010" || !strings.Contains(b.Reason, "behind latest on disk 00010") {
		t.Errorf("behind: %+v, %v (a longer number is later, whatever its digits)", b, err)
	}
	set("00010", true)
	b, err = MigrationsPending(ctx, db, dir)
	if err != nil || b == nil || !b.Dirty || b.Version != "00010" || !strings.Contains(b.Reason, "marked dirty") {
		t.Errorf("dirty: %+v, %v", b, err)
	}
	set("00010", false)
	if b, err := MigrationsPending(ctx, db, dir); err != nil || b != nil {
		t.Errorf("up to date: %+v, %v", b, err)
	}
	set("00011", false)
	if b, err := MigrationsPending(ctx, db, dir); err != nil || b != nil {
		t.Errorf("ahead of the files on disk is not pending: %+v, %v", b, err)
	}
	if _, err := db.Exec(`TRUNCATE schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if b, err := MigrationsPending(ctx, db, dir); err != nil || b == nil || !strings.Contains(b.Reason, "no migrations applied") {
		t.Errorf("an empty schema_migrations: %+v, %v", b, err)
	}
}

func TestIntrospection_ReportsAFailingDatabase(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := IntrospectEnumPools(ctx, db); err == nil {
		t.Error("IntrospectEnumPools over a closed database must fail")
	}
	if _, err := IntrospectCheckBounds(ctx, db); err == nil {
		t.Error("IntrospectCheckBounds over a closed database must fail")
	}
	if got := singleCheckColumn(ctx, db, "t", []byte("{1}")); got != "" {
		t.Errorf("a failing lookup resolves no column, got %q", got)
	}
}

func TestIntrospectCheckBounds_FoldsEveryRangeSpelling(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(`
CREATE TABLE ranged (
    id INT PRIMARY KEY,
    age INT CHECK (age >= 0) CHECK (age <= 150),
    score INT CHECK (score BETWEEN 1 AND 5),
    rate NUMERIC CHECK (rate >= 0.5) CHECK (rate <= 9.5),
    flag INT CHECK (flag > 0),
    tight INT CHECK (tight >= 1) CHECK (tight >= 3) CHECK (tight <= 9) CHECK (tight <= 7),
    status TEXT CHECK (status IN ('a', 'b')),
    CONSTRAINT two_cols CHECK (age >= score)
)`); err != nil {
		t.Fatal(err)
	}
	b, err := IntrospectCheckBounds(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int64{"age": {0, 150}, "score": {1, 5}, "rate": {1, 9}, "flag": {1, -1}, "tight": {3, 7}}
	for col, w := range want {
		got, ok := b.get("ranged", col)
		if !ok {
			t.Errorf("%s: no bound read", col)
			continue
		}
		if got.Min == nil || *got.Min != w[0] {
			t.Errorf("%s: min = %v, want %d", col, got.Min, w[0])
		}
		if w[1] == -1 {
			if got.Max != nil {
				t.Errorf("%s: a strict lower bound has no max, got %d", col, *got.Max)
			}
		} else if got.Max == nil || *got.Max != w[1] {
			t.Errorf("%s: max = %v, want %d (a fractional bound rounds inward)", col, got.Max, w[1])
		}
	}
	if _, ok := b.get("ranged", "status"); ok {
		t.Error("an enum pool is not a numeric range")
	}
}

func TestIntrospectEnumPools_SkipsWhatIsNotASingleColumnPool(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(`
CREATE TYPE mood AS ENUM ('calm', 'angry');
CREATE TABLE people (
    id INT PRIMARY KEY,
    mood mood NOT NULL,
    tier TEXT CHECK (tier IN ('free', 'pro')),
    solo TEXT CHECK (solo = 'only'),
    n INT CHECK (n = 7),
    name TEXT CHECK (name <> ''),
    a TEXT, b TEXT,
    CONSTRAINT pair CHECK (a IN ('x', 'y') OR b IN ('z'))
)`); err != nil {
		t.Fatal(err)
	}
	pools, err := IntrospectEnumPools(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for col, want := range map[string][]string{"mood": {"calm", "angry"}, "tier": {"free", "pro"}, "solo": {"only"}} {
		if got, ok := pools.get("people", col); !ok || !sameSet(got, want) {
			t.Errorf("%s pool = %v (%v), want %v", col, got, ok, want)
		}
	}
	for _, col := range []string{"name", "a", "b"} {
		if got, ok := pools.get("people", col); ok {
			t.Errorf("%s is not a single-column pool, got %v", col, got)
		}
	}
}

func TestSeedGraphErr_ReportsWhatWentWrong(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db := setupGraphDB(t)
	if _, err := SeedGraphErr(ctx, StdDB(db), "no_such_table"); err == nil || !strings.Contains(err.Error(), "no table") || !strings.Contains(err.Error(), "cart_items") {
		t.Errorf("an unknown root names the known tables: %v", err)
	}
	closed, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := SeedGraphErr(ctx, StdDB(closed), "x"); err == nil || !strings.Contains(err.Error(), "introspect schema") {
		t.Errorf("a closed database: %v", err)
	}

	g, err := SeedGraphErr(ctx, StdDB(db), "cart_items")
	if err != nil {
		t.Fatal(err)
	}
	if g.Root() != "cart_items" {
		t.Errorf("Root = %q", g.Root())
	}
	tables := g.Tables()
	if want := []string{"cart_items", "carts", "products"}; !sameSet(tables, want) {
		t.Errorf("Tables = %v, want %v", tables, want)
	}
	tables[0] = "mutated"
	if g.Tables()[0] == "mutated" {
		t.Error("Tables must return a copy")
	}
	// The same graph seeded again collides on its own keys: rows already there.
	if _, err := SeedGraphErr(ctx, StdDB(db), "cart_items"); err != nil {
		t.Logf("re-seeding: %v", err)
	}
}

func TestSeededGraph_AccessorsPanicOnTheWrongNameWithTheListOfWhatExists(t *testing.T) {
	requirePG(t)
	db := setupGraphDB(t)
	g, err := SeedGraphErr(context.Background(), StdDB(db), "cart_items")
	if err != nil {
		t.Fatal(err)
	}
	panicText := func(f func()) (msg string) {
		defer func() { msg = fmt.Sprint(recover()) }()
		f()
		return "no panic"
	}
	if got := panicText(func() { g.PK("shipments") }); !strings.Contains(got, `"shipments" is not in the graph`) || !strings.Contains(got, "cart_items") {
		t.Errorf("PK of a table off the spine: %s", got)
	}
	if got := panicText(func() { g.Value("shipments", "id") }); !strings.Contains(got, `"shipments" is not in the graph`) {
		t.Errorf("Value of a table off the spine: %s", got)
	}
	if got := panicText(func() { g.Value("carts", "nope") }); !strings.Contains(got, `column "nope" was not seeded on table "carts"`) {
		t.Errorf("Value of a missing column: %s", got)
	}
	if v := g.Value("carts", "label"); v == "" {
		t.Error("a seeded column has its value")
	}
}

// failingDB is a DB whose statements fail on demand.
type failingDB struct {
	inner   DB
	failOn  string
	err     error
	history []string
}

func (f *failingDB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	f.history = append(f.history, strings.Fields(q)[0])
	if f.failOn != "" && strings.HasPrefix(strings.TrimSpace(q), f.failOn) {
		return nil, f.err
	}
	return f.inner.Exec(ctx, q, args...)
}

func (f *failingDB) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return f.inner.Query(ctx, q, args...)
}

func TestApplyTo_RollsBackOnAFailedInsertAndReportsAFailedBeginOrCommit(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db := setupGraphDB(t)
	boom := errors.New("connection reset")

	tables, err := schemadef.Introspect(queryer{StdDB(db)})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planFromTables(ctx, queryer{StdDB(db)}, tables, Config{Rows: 1})
	if err != nil {
		t.Fatal(err)
	}

	f := &failingDB{inner: StdDB(db), failOn: "BEGIN", err: boom}
	if err := applyTo(ctx, f, plan); err == nil || !errors.Is(err, boom) || !strings.Contains(err.Error(), "begin seed transaction") {
		t.Errorf("a failed BEGIN: %v", err)
	}
	f = &failingDB{inner: StdDB(db), failOn: "COMMIT", err: boom}
	if err := applyTo(ctx, f, plan); err == nil || !errors.Is(err, boom) || !strings.Contains(err.Error(), "commit") {
		t.Errorf("a failed COMMIT: %v", err)
	}
	if _, err := db.Exec("ROLLBACK"); err != nil {
		t.Logf("cleanup rollback: %v", err)
	}

	f = &failingDB{inner: StdDB(db), failOn: "INSERT", err: errors.New(`ERROR: duplicate key value violates unique constraint "products_pkey" (SQLSTATE 23505)`)}
	err = applyTo(ctx, f, plan)
	if err == nil || !strings.Contains(err.Error(), "seed ") {
		t.Fatalf("a failed INSERT: %v", err)
	}
	if last := f.history[len(f.history)-1]; last != "ROLLBACK" {
		t.Errorf("statements = %v, want a ROLLBACK after the failed INSERT", f.history)
	}
}
