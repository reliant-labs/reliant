package orm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// newBareClient is a client on a fresh, empty pgtest database.
func newBareClient(t *testing.T) *Client {
	t.Helper()
	if testing.Short() {
		t.Skip("boots postgres; skipped under -short")
	}
	sqldb, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	c, err := NewClientWithDB(sqldb, "postgres")
	if err != nil {
		t.Fatalf("NewClientWithDB: %v", err)
	}
	return c
}

func tableExistsIn(t *testing.T, c *Client, name string) bool {
	t.Helper()
	var ok bool
	if err := c.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name=$1)`, name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// Migrate applies pending migrations in version order, each in its own
// transaction together with its bookkeeping row, and skips the applied ones on
// the next run.
func TestMigrationManager_MigrateAppliesPendingInVersionOrder(t *testing.T) {
	c := newBareClient(t)
	ctx := context.Background()
	m := NewMigrationManager(c)

	var order []string
	track := func(v, sqlText string) *Migration {
		return &Migration{Version: v, Description: "desc " + v, Up: func(ctx context.Context, db Context) error {
			order = append(order, v)
			_, err := db.Exec(ctx, sqlText)
			return err
		}}
	}
	// Registered out of order on purpose.
	if err := m.RegisterMany(
		track("002", `CREATE TABLE widgets_b (id INT)`),
		track("001", `CREATE TABLE widgets_a (id INT)`),
		track("003", `CREATE TABLE widgets_c (id INT)`),
	); err != nil {
		t.Fatal(err)
	}

	if err := m.MigrateTo(ctx, "002"); err != nil {
		t.Fatalf("MigrateTo(002): %v", err)
	}
	if got := strings.Join(order, ","); got != "001,002" {
		t.Fatalf("MigrateTo(002) ran %q, want 001,002 and not 003", got)
	}
	if tableExistsIn(t, c, "widgets_c") {
		t.Fatal("MigrateTo(002) ran migration 003")
	}

	st, err := m.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var applied []bool
	for _, s := range st {
		applied = append(applied, s.Applied)
		if s.Applied == s.AppliedAt.IsZero() {
			t.Errorf("%s: Applied=%v AppliedAt=%v", s.Version, s.Applied, s.AppliedAt)
		}
	}
	if len(st) != 3 || st[0].Version != "001" || st[2].Version != "003" ||
		!applied[0] || !applied[1] || applied[2] || st[0].Description != "desc 001" {
		t.Fatalf("Status = %+v", st)
	}

	order = nil
	if err := m.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := strings.Join(order, ","); got != "003" {
		t.Fatalf("Migrate ran %q, want only the pending 003", got)
	}
	order = nil
	if err := m.Migrate(ctx); err != nil || len(order) != 0 {
		t.Fatalf("a second Migrate ran %v (err %v), want nothing", order, err)
	}
}

// A failing migration rolls back with its bookkeeping row: nothing it did
// survives, it is not recorded, and the error names it.
func TestMigrationManager_FailedMigrationRollsBackAndIsNotRecorded(t *testing.T) {
	c := newBareClient(t)
	ctx := context.Background()
	m := NewMigrationManager(c)
	boom := errors.New("boom")
	if err := m.RegisterMany(
		&Migration{Version: "001", Up: func(ctx context.Context, db Context) error {
			if _, err := db.Exec(ctx, `CREATE TABLE half_done (id INT)`); err != nil {
				return err
			}
			return boom
		}},
		&Migration{Version: "002", Up: upNoop},
	); err != nil {
		t.Fatal(err)
	}

	for name, run := range map[string]func() error{
		"Migrate":   func() error { return m.Migrate(ctx) },
		"MigrateTo": func() error { return m.MigrateTo(ctx, "999") },
	} {
		err := run()
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), "migration 001 failed") {
			t.Fatalf("%s err = %v, want the wrapped boom naming 001", name, err)
		}
	}
	if tableExistsIn(t, c, "half_done") {
		t.Fatal("the failed migration's DDL survived the rollback")
	}
	st, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range st {
		if s.Applied {
			t.Fatalf("%s recorded as applied after a failure", s.Version)
		}
	}
}

// A migration that cannot be recorded (the tracking table is unusable) fails
// loudly instead of being run again forever.
func TestMigrationManager_QuotesTrackingTableAndWrapsDatabaseFailures(t *testing.T) {
	c := newBareClient(t)
	ctx := context.Background()
	m := NewMigrationManager(c)
	m.SetTableName(`bad"; DROP TABLE x; --`)
	// The name is quoted, so it is just an odd table name, and works.
	if err := m.Register(NewSQLMigration("001", "x", "SELECT 1")); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatalf("Migrate with an odd table name: %v", err)
	}

	// A closed pool makes every database step fail; each entry point wraps it.
	closed := newBareClient(t)
	cm := NewMigrationManager(closed)
	if err := closed.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := cm.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "failed to create migrations table") {
		t.Errorf("Migrate on a closed pool = %v", err)
	}
	if err := cm.MigrateTo(ctx, "1"); err == nil || !strings.Contains(err.Error(), "failed to create migrations table") {
		t.Errorf("MigrateTo on a closed pool = %v", err)
	}
	if _, err := cm.Status(ctx); err == nil || !strings.Contains(err.Error(), "failed to create migrations table") {
		t.Errorf("Status on a closed pool = %v", err)
	}
}

func TestMigrationManager_SchemaCreateMigrationCreatesTablesInOrder(t *testing.T) {
	c := newBareClient(t)
	ctx := context.Background()
	m := NewMigrationManager(c)
	if err := m.Register(NewSchemaCreateMigration("001", usersNow(), postsNew())); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	diffs, err := DiffDatabase(ctx, c, c.Dialect(), []TableSchema{usersNow(), postsNew()})
	if err != nil || len(diffs) != 0 {
		t.Fatalf("diffs after the create migration = %+v, %v; want none", diffs, err)
	}

	// A bad schema surfaces as an error naming the table.
	bad := NewSchemaCreateMigration("002", TableSchema{Name: "broken", Fields: []FieldSchema{{Name: "id", Type: "NOT_A_TYPE"}}})
	err = c.RunTransaction(ctx, ReadCommitted, func(tx Context) error { return bad.Up(ctx, tx) })
	if err == nil || !strings.Contains(err.Error(), "failed to create table broken") {
		t.Fatalf("bad schema migration err = %v", err)
	}
}

// GenerateMigration returns a migration carrying exactly the statements the
// diff calls for, and refuses when there is nothing to do.
func TestMigrationManager_GenerateMigration(t *testing.T) {
	c := newDiffTestClient(t)
	ctx := context.Background()
	m := NewMigrationManager(c)

	if _, err := m.GenerateMigration(ctx, []TableSchema{usersNow()}); err == nil || err.Error() != "no schema changes detected" {
		t.Fatalf("in-sync GenerateMigration = %v", err)
	}
	mig, err := m.GenerateMigration(ctx, []TableSchema{usersNext(), postsNew()})
	if err != nil {
		t.Fatalf("GenerateMigration: %v", err)
	}
	if mig.Description != "Auto-generated schema migration" || !strings.HasPrefix(mig.Version, "auto_") {
		t.Fatalf("migration = %+v", mig)
	}
	if err := m.Register(mig); err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(ctx); err != nil {
		t.Fatalf("Migrate of the generated migration: %v", err)
	}
	if diffs, err := DiffDatabase(ctx, c, c.Dialect(), []TableSchema{usersNext(), postsNew()}); err != nil || len(diffs) != 0 {
		t.Fatalf("diffs after applying the generated migration = %+v, %v", diffs, err)
	}

	// Destructive diffs are refused, not generated.
	if _, err := m.GenerateMigration(ctx, []TableSchema{{Name: "users", Fields: usersNow().Fields[:2]}}); !errors.Is(err, ErrDestructiveOperation) {
		t.Fatalf("destructive GenerateMigration err = %v, want ErrDestructiveOperation", err)
	}
	if _, err := m.PlanMigration(ctx, []TableSchema{{Name: "users", Fields: usersNow().Fields[:2]}}); !errors.Is(err, ErrDestructiveOperation) {
		t.Fatalf("destructive PlanMigration err = %v, want ErrDestructiveOperation", err)
	}
	if err := m.AutoMigrate(ctx, []TableSchema{{Name: "users", Fields: usersNow().Fields[:2]}}); !errors.Is(err, ErrDestructiveOperation) {
		t.Fatalf("destructive AutoMigrate err = %v, want ErrDestructiveOperation", err)
	}
}

// A paired migration runs its schema changes and then its data migration in one
// transaction: when the data step fails, the schema change rolls back too.
func TestMigrationManager_PairedMigrationIsOneTransaction(t *testing.T) {
	c := newDiffTestClient(t)
	ctx := context.Background()

	run := func(version string, data *Migration) error {
		m := NewMigrationManager(c)
		if err := m.RegisterPairedMigration(&PairedMigration{
			Version:       version,
			SchemaChanges: []TableSchema{usersNext(), postsNew()},
			DataMigration: data,
		}); err != nil {
			t.Fatal(err)
		}
		return m.Migrate(ctx)
	}

	boom := errors.New("data step failed")
	err := run("p1", &Migration{Up: func(ctx context.Context, db Context) error { return boom }})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "data migration failed") {
		t.Fatalf("err = %v, want the data migration failure", err)
	}
	if tableExistsIn(t, c, "posts") {
		t.Fatal("the schema change survived a failed data migration")
	}

	if err := run("p2", &Migration{Up: func(ctx context.Context, db Context) error {
		_, err := db.Exec(ctx, `INSERT INTO users (id, email, age) VALUES (1, 'a@b.c', 30)`)
		return err
	}}); err != nil {
		t.Fatalf("paired Migrate: %v", err)
	}
	var age int
	if err := c.QueryRow(ctx, `SELECT age FROM users WHERE id = 1`).Scan(&age); err != nil || age != 30 {
		t.Fatalf("the data step ran against the migrated schema: age=%d err=%v", age, err)
	}
	if !tableExistsIn(t, c, "posts") {
		t.Fatal("the new table was not created")
	}
}

func TestIntrospectAllTables_And_ValidateSchema(t *testing.T) {
	c := newDiffTestClient(t)
	ctx := context.Background()
	if _, err := c.Exec(ctx, `CREATE TABLE other (k BIGINT PRIMARY KEY, v JSONB NOT NULL DEFAULT '{}')`); err != nil {
		t.Fatal(err)
	}

	all, err := IntrospectAllTables(ctx, c, c.Dialect())
	if err != nil {
		t.Fatalf("IntrospectAllTables: %v", err)
	}
	var names []string
	for _, s := range all {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "other,users" {
		t.Fatalf("tables = %v, want [other users] in name order", names)
	}
	other := all[0]
	if len(other.Fields) != 2 || other.Fields[0].Name != "k" || !other.Fields[0].PrimaryKey || other.Fields[0].Type != TypeBigInt {
		t.Errorf("other.k = %+v", other.Fields)
	}
	if v := other.Fields[1]; v.Type != TypeJSONB || !v.NotNull || !strings.Contains(v.DefaultValue, "'{}'") {
		t.Errorf("other.v = %+v", v)
	}

	// ValidateSchema is nil in sync and lists every kind of drift otherwise.
	if err := ValidateSchema(ctx, c, usersNow()); err != nil {
		t.Fatalf("in-sync ValidateSchema: %v", err)
	}
	drifted := usersNext()
	drifted.Fields = append(drifted.Fields[:2], drifted.Fields[3:]...) // no `name`, but age/created_at added
	err = ValidateSchema(ctx, c, drifted)
	if err == nil {
		t.Fatal("ValidateSchema accepted a drifted table")
	}
	for _, want := range []string{"schema mismatch for table users", "missing column created_at", "extra column name", "missing index idx_users_name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateSchema error missing %q: %v", want, err)
		}
	}
	if err := ValidateSchema(ctx, c, TableSchema{Name: "ghost"}); err == nil || !strings.Contains(err.Error(), "failed to introspect table ghost") {
		t.Errorf("ValidateSchema on a missing table = %v", err)
	}

	// A column the schema calls TEXT but the database holds as INTEGER.
	if err := ValidateSchema(ctx, c, TableSchema{Name: "users", Fields: []FieldSchema{{Name: "id", Type: TypeText}}}); err == nil {
		t.Error("ValidateSchema accepted an incompatible column type")
	}
}

func TestIntrospectTable_Errors(t *testing.T) {
	c := newDiffTestClient(t)
	ctx := context.Background()
	d := c.Dialect()

	if _, err := IntrospectTable(nil, c, d, "users"); !errors.Is(err, ErrNilContext) { //nolint:staticcheck // the nil ctx is the case under test
		t.Errorf("nil ctx err = %v, want ErrNilContext", err)
	}
	if _, err := IntrospectAllTables(nil, c, d); !errors.Is(err, ErrNilContext) { //nolint:staticcheck // as above
		t.Errorf("IntrospectAllTables nil ctx err = %v, want ErrNilContext", err)
	}
	if _, err := IntrospectTable(ctx, c, d, ""); err == nil || !strings.Contains(err.Error(), "table name cannot be empty") {
		t.Errorf("empty table name err = %v", err)
	}
	_, err := IntrospectTable(ctx, c, d, "ghost")
	var se *SchemaError
	if !errors.As(err, &se) || se.Table != "ghost" || !strings.Contains(err.Error(), "table does not exist") {
		t.Errorf("missing table err = %v, want a SchemaError for ghost", err)
	}
	if _, err := IntrospectTable(ctx, c, &otherDialect{}, "users"); !errors.Is(err, ErrInvalidDialect) {
		t.Errorf("unsupported dialect err = %v, want ErrInvalidDialect", err)
	}
	if _, err := IntrospectAllTables(ctx, c, &otherDialect{}); err == nil || !strings.Contains(err.Error(), "unsupported dialect for introspection: other") {
		t.Errorf("IntrospectAllTables on an unsupported dialect = %v", err)
	}
	// A column type the ORM has no FieldType for is reported, not skipped.
	if _, err := c.Exec(ctx, `CREATE TABLE weird (id INT PRIMARY KEY, ts TSVECTOR)`); err != nil {
		t.Fatal(err)
	}
	if _, err := IntrospectTable(ctx, c, d, "weird"); err == nil || !strings.Contains(err.Error(), "ts") {
		t.Errorf("unknown column type err = %v, want it to name the column", err)
	}
}

func TestPostgresDialect_ScanColumnAndIndexOnRealRows(t *testing.T) {
	// ScanColumn / ScanIndex read what the dialect's own introspection queries
	// select, on a real database.
	c := newDiffTestClient(t)
	ctx := context.Background()
	d := &PostgresDialect{}

	rows, err := c.Query(ctx, d.IntrospectColumnsQuery("users"))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []IntrospectedColumn
	for rows.Next() {
		col, err := d.ScanColumn(rows)
		if err != nil {
			t.Fatalf("ScanColumn: %v", err)
		}
		cols = append(cols, col)
	}
	if len(cols) != 3 || cols[0].Name != "id" || !cols[0].IsPrimaryKey || cols[0].Type != TypeInteger ||
		cols[1].Name != "email" || cols[1].Nullable || cols[2].Name != "name" || !cols[2].Nullable {
		t.Fatalf("columns = %+v", cols)
	}

	irows, err := c.Query(ctx, d.IntrospectIndexesQuery("users"))
	if err != nil {
		t.Fatal(err)
	}
	defer irows.Close()
	if !irows.Next() {
		t.Fatal("no index row")
	}
	name, col, unique, err := d.ScanIndex(irows)
	if err != nil || name != "idx_users_email" || col != "email" || unique {
		t.Fatalf("ScanIndex = %q %q %v %v", name, col, unique, err)
	}

	// A row of the wrong shape is an error naming what failed to scan.
	bad, err := c.Query(ctx, `SELECT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Close()
	bad.Next()
	if _, err := d.ScanColumn(bad); err == nil || !strings.Contains(err.Error(), "failed to scan column row") {
		t.Errorf("ScanColumn on a short row = %v", err)
	}
	bad2, err := c.Query(ctx, `SELECT 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer bad2.Close()
	bad2.Next()
	if _, _, _, err := d.ScanIndex(bad2); err == nil || !strings.Contains(err.Error(), "failed to scan index row") {
		t.Errorf("ScanIndex on a short row = %v", err)
	}
	for _, q := range []struct{ sql, want string }{
		{`SELECT '', 'c', true`, "scanned index name is empty"},
		{`SELECT 'i', '', true`, "scanned column name is empty for index i"},
	} {
		r, err := c.Query(ctx, q.sql)
		if err != nil {
			t.Fatal(err)
		}
		r.Next()
		_, _, _, err = d.ScanIndex(r)
		_ = r.Close()
		if err == nil || !strings.Contains(err.Error(), q.want) {
			t.Errorf("ScanIndex(%s) = %v, want %q", q.sql, err, q.want)
		}
	}
	// A column type with neither udt nor data type mapping fails with the type.
	r, err := c.Query(ctx, `SELECT 'c', 'tsvector', 'tsvector', 'YES', NULL::text, false, false`)
	if err != nil {
		t.Fatal(err)
	}
	r.Next()
	_, err = d.ScanColumn(r)
	_ = r.Close()
	if err == nil || !strings.Contains(err.Error(), "tsvector") {
		t.Errorf("ScanColumn on an unmapped type = %v", err)
	}
}
