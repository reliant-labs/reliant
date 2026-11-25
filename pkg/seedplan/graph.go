package seedplan

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// BuildPlanFromDB builds a seed plan for a database that is ALREADY migrated,
// by introspecting it directly. No migrations directory, no shadow database,
// no second postgres: the schema is read from the handle the caller passes.
//
// This is the counterpart to BuildLivePlan, and the difference is only WHERE
// the schema comes from. BuildLivePlan starts from migration FILES, so it has
// to apply them somewhere first, and that somewhere is the shadow. A caller
// holding a migrated database has already paid that cost, and asking it to
// pay again — to boot a throwaway postgres describing the schema it is
// connected to — would be the tail wagging the dog.
//
// The vocabulary overlay is not read here, because there is no project
// directory to read it from. Callers wanting the project's domain words on a
// live-schema plan load them and call plan.ApplyVocab themselves.
func BuildPlanFromDB(ctx context.Context, db *sql.DB, cfg Config) (*Plan, error) {
	tables, err := schemadef.Introspect(db)
	if err != nil {
		return nil, fmt.Errorf("introspect live schema: %w", err)
	}
	return planFromTables(ctx, db, tables, cfg)
}

// planFromTables is the shared tail of BuildLivePlan and BuildPlanFromDB:
// given the schema model and the target database, read the pools/bounds that
// only the live database can answer and build the plan.
func planFromTables(ctx context.Context, db schemadef.Queryer, tables []schemadef.Table, cfg Config) (*Plan, error) {
	if cfg.Tables != nil {
		tables = ScopeTables(tables, cfg.Tables)
	}
	if len(tables) == 0 {
		return emptyPlan(cfg), nil
	}
	pools, err := IntrospectEnumPools(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read enum/check pools: %w", err)
	}
	bounds, err := IntrospectCheckBounds(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read range CHECK bounds: %w", err)
	}
	plan, err := BuildPlan(tables, pools, cfg)
	if err != nil {
		return nil, err
	}
	plan.bounds = bounds
	return plan, nil
}

// applyTo executes plan's INSERTs through a DB handle, in the plan's
// topological order, inside one transaction.
//
// It is Apply's counterpart for a caller holding an orm.Context rather than a
// *sql.DB. The transaction is explicit BEGIN/COMMIT rather than sql.Tx,
// because the DB interface deliberately has no Begin: the handle a forge test
// owns may itself already be transactional, and this way the same two
// statements work either way. Atomicity is the point — a spine that half-lands
// leaves a test staring at foreign keys that resolve to nothing.
func applyTo(ctx context.Context, db DB, plan *Plan) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if _, err := db.Exec(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("begin seed transaction: %w", err)
	}
	for _, tp := range plan.tables {
		stmt := plan.statement(tp)
		if stmt == "" {
			continue
		}
		if _, err := db.Exec(ctx, stmt); err != nil {
			_, _ = db.Exec(ctx, "ROLLBACK")
			return plan.explainInsertFailure(tp, err)
		}
	}
	if _, err := db.Exec(ctx, "COMMIT"); err != nil {
		_, _ = db.Exec(ctx, "ROLLBACK")
		return fmt.Errorf("commit seed transaction: %w", err)
	}
	return nil
}

// emptyPlan is the zero plan for a schema with no tables — seeding it is a
// no-op rather than an error.
func emptyPlan(cfg Config) *Plan {
	return &Plan{
		cfg:        cfg,
		byName:     map[string]schemadef.Table{},
		rowsOf:     map[string]int{},
		rowsTarget: map[string]int{},
	}
}

// FKClosure returns rootTable plus every table reachable from it over declared
// foreign keys — its transitive ancestors, root INCLUDED — restricted to
// tables present in the model. Self-references and edges back into the closure
// resolve per-row inside BuildPlan, so they are not walked. The result is
// sorted, so a closure is stable across runs.
//
// Ancestors, not descendants: the closure answers "what must already exist for
// a row of rootTable to be insertable", which is exactly the set a foreign key
// makes mandatory. A child table is never dragged in, because nothing requires
// a row to have children.
func FKClosure(tables []schemadef.Table, rootTable string) []string {
	byName := make(map[string]schemadef.Table, len(tables))
	for _, t := range tables {
		byName[t.Name] = t
	}
	if _, ok := byName[rootTable]; !ok {
		return nil
	}
	seen := map[string]bool{rootTable: true}
	var walk func(string)
	walk = func(name string) {
		for _, fk := range byName[name].ForeignKeys {
			if fk.RefTable == name || seen[fk.RefTable] {
				continue
			}
			if _, ok := byName[fk.RefTable]; !ok {
				continue
			}
			seen[fk.RefTable] = true
			walk(fk.RefTable)
		}
	}
	walk(rootTable)
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TB is the subset of *testing.T the seeding helpers use. It is declared here
// rather than taking testing.TB so this package does not force the `testing`
// import on a non-test caller that wants the same behavior.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// DB is the database handle SeedGraph seeds into: read a catalog, run an
// INSERT. It is deliberately the shape forge's own orm.Context already has,
// so the handle a flow test is holding — app.NewMigratedTestDB(t) — can be
// passed straight in with no unwrapping, no type assertion, and no import of
// the ORM from this package. A raw *sql.DB has the ...Context spelling of
// these methods instead; wrap it with StdDB.
type DB interface {
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// StdDB adapts a database/sql handle to DB, for a caller holding a *sql.DB
// rather than a forge orm.Context.
func StdDB(db *sql.DB) DB { return stdDB{db} }

type stdDB struct{ db *sql.DB }

func (s stdDB) Exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, q, args...)
}

func (s stdDB) Query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, q, args...)
}

// queryer adapts a DB to the ...Context spelling schemadef.Introspect wants.
type queryer struct{ db DB }

func (q queryer) QueryContext(ctx context.Context, s string, args ...any) (*sql.Rows, error) {
	return q.db.Query(ctx, s, args...)
}

// SeededGraph is the handle SeedGraph returns: the row-0 primary keys and
// column values of the seeded root and its foreign-key ancestors, so a test
// can reference the rows it just created.
type SeededGraph struct {
	root   string
	tables []string
	pk     map[string]string
	values map[string]map[string]string
}

// SeedGraph seeds rootTable and its transitive foreign-key ancestors into db
// as one connected happy-path spine — row 0 of every table references row 0 of
// its parents — and returns a handle to the seeded rows.
//
// db is the caller's OWN already-migrated database (in a forge project,
// app.NewMigratedTestDB(t)). The schema is introspected from it, the plan is
// computed from that schema, and the INSERTs run against it in one
// transaction. Every seeded row satisfies the migrations' CHECK / enum /
// char_length / range constraints, because the values come from forge's seed
// planner — the same one behind `forge db seed` — evaluated now, against the
// schema this database actually has.
//
// Root at the deepest table you want pre-seeded. To test a CREATE path, root
// at the PARENT (the spine is seeded; you create the child through its own
// RPC, referencing g.PK("<parent>")). To test a read/derived/update path, root
// at the leaf, so the whole graph is present.
//
//	g := seedplan.SeedGraph(t, db, "cart_items")
//	resp, err := svc.PlaceOrder(ctx, connect.NewRequest(&pb.PlaceOrderRequest{
//	    CartId: g.PK("carts"),
//	}))
func SeedGraph(t TB, db DB, rootTable string) *SeededGraph {
	t.Helper()
	g, err := SeedGraphErr(context.Background(), db, rootTable)
	if err != nil {
		t.Fatalf("SeedGraph(%q): %v", rootTable, err)
		return nil
	}
	return g
}

// SeedGraphErr is SeedGraph for a caller that wants the error rather than a
// failed test — the same work, without the testing-shaped wrapper.
func SeedGraphErr(ctx context.Context, db DB, rootTable string) (*SeededGraph, error) {
	tables, err := schemadef.Introspect(queryer{db})
	if err != nil {
		return nil, fmt.Errorf("introspect schema: %w", err)
	}
	closure := FKClosure(tables, rootTable)
	if len(closure) == 0 {
		return nil, fmt.Errorf("no table %q in this database (known tables: %s)", rootTable, tableNames(tables))
	}

	inClosure := make(map[string]bool, len(closure))
	for _, n := range closure {
		inClosure[n] = true
	}
	sub := make([]schemadef.Table, 0, len(closure))
	for _, tbl := range tables {
		if inClosure[tbl.Name] {
			sub = append(sub, tbl)
		}
	}

	// One connected spine: row 0 of every table references row 0 of its
	// parents. Rows:1 keeps the seeded graph minimal and deterministic — a
	// test drives any further rows through the API under test.
	plan, err := planFromTables(ctx, queryer{db}, sub, Config{Rows: 1, Salt: 0})
	if err != nil {
		return nil, err
	}
	if err := applyTo(ctx, db, plan); err != nil {
		return nil, err
	}

	g := &SeededGraph{
		root:   rootTable,
		tables: closure,
		pk:     map[string]string{},
		values: map[string]map[string]string{},
	}
	byName := make(map[string]schemadef.Table, len(sub))
	for _, tbl := range sub {
		byName[tbl.Name] = tbl
	}
	for _, n := range closure {
		tbl := byName[n]
		if len(tbl.PKCols) >= 1 {
			if v, ok := plan.SeedValue(n, tbl.PKCols[0], 0); ok {
				g.pk[n] = v
			}
		}
		cols := map[string]string{}
		for _, c := range tbl.Columns {
			if v, ok := plan.SeedValue(n, c.Name, 0); ok {
				cols[c.Name] = v
			}
		}
		g.values[n] = cols
	}
	return g, nil
}

// Root is the table SeedGraph was rooted at.
func (g *SeededGraph) Root() string { return g.root }

// Tables lists the tables that were seeded — the root and its foreign-key
// ancestors, sorted.
func (g *SeededGraph) Tables() []string { return append([]string(nil), g.tables...) }

// PK returns the seeded row-0 primary-key value of table — hand it to an RPC
// to reference the seeded row. Panics if table was not part of this graph,
// which is a test-authoring error (the wrong table name), not a runtime
// condition worth returning.
func (g *SeededGraph) PK(table string) string {
	v, ok := g.pk[table]
	if !ok {
		panic(fmt.Sprintf("SeededGraph.PK: table %q is not in the graph rooted at %q (seeded: %v)", table, g.root, g.tables))
	}
	return v
}

// Value returns the seeded row-0 value of table.column, so an assertion reads
// the REAL seeded input instead of hard-coding a copy of it. Panics if the
// table or column was not seeded.
func (g *SeededGraph) Value(table, column string) string {
	cols, ok := g.values[table]
	if !ok {
		panic(fmt.Sprintf("SeededGraph.Value: table %q is not in the graph rooted at %q (seeded: %v)", table, g.root, g.tables))
	}
	v, ok := cols[column]
	if !ok {
		panic(fmt.Sprintf("SeededGraph.Value: column %q was not seeded on table %q", column, table))
	}
	return v
}

// tableNames lists the model's table names for an error message.
func tableNames(tables []schemadef.Table) string {
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return fmt.Sprint(names)
}
