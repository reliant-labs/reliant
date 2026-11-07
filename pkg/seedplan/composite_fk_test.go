package seedplan

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The multi-tenant integrity pattern: every child carries tenant_id, and a
// composite foreign key makes "a child references only a parent in its own
// tenant" a database fact — beside the single-column references the tables
// were born with.
const compositeFKMigration = `
CREATE TABLE tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL
);

CREATE TABLE strategies (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    title TEXT NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE channels (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    title TEXT NOT NULL,
    UNIQUE (tenant_id, id)
);

CREATE TABLE hypotheses (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    strategy_id TEXT NOT NULL REFERENCES strategies(id),
    channel_id TEXT REFERENCES channels(id),
    claim TEXT NOT NULL,
    FOREIGN KEY (tenant_id, strategy_id) REFERENCES strategies (tenant_id, id),
    FOREIGN KEY (tenant_id, channel_id) REFERENCES channels (tenant_id, id)
);

CREATE TABLE nodes (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    parent_id TEXT,
    UNIQUE (tenant_id, id),
    FOREIGN KEY (tenant_id, parent_id) REFERENCES nodes (tenant_id, id)
);
`

// compositeFKSchema is compositeFKMigration as introspection reports it: the
// composites one entry per column, each carrying the whole key.
func compositeFKSchema() []schemadef.Table {
	tenants := schemadef.Table{
		Name:    "tenants",
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{col("id", schemadef.TypeString, true, true), col("name", schemadef.TypeString, true, false)},
	}
	parent := func(name string) schemadef.Table {
		return schemadef.Table{
			Name:   name,
			PKCols: []string{"id"},
			Columns: []schemadef.Column{
				col("id", schemadef.TypeString, true, true),
				col("tenant_id", schemadef.TypeString, true, false),
				col("title", schemadef.TypeString, true, false),
			},
			ForeignKeys: []schemadef.ForeignKey{
				{Column: "tenant_id", RefTable: "tenants", RefColumn: "id", Name: name + "_tenant_id_fkey"},
			},
		}
	}
	composite := func(name, refTable, col string) []schemadef.ForeignKey {
		keys, refs := []string{"tenant_id", col}, []string{"tenant_id", "id"}
		return []schemadef.ForeignKey{
			{Column: "tenant_id", RefTable: refTable, RefColumn: "tenant_id", Name: name, KeyColumns: keys, RefKeyColumns: refs},
			{Column: col, RefTable: refTable, RefColumn: "id", Name: name, KeyColumns: keys, RefKeyColumns: refs},
		}
	}
	hypotheses := schemadef.Table{
		Name:   "hypotheses",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("tenant_id", schemadef.TypeString, true, false),
			col("strategy_id", schemadef.TypeString, true, false),
			col("channel_id", schemadef.TypeString, false, false),
			col("claim", schemadef.TypeString, true, false),
		},
		ForeignKeys: append(append([]schemadef.ForeignKey{
			{Column: "tenant_id", RefTable: "tenants", RefColumn: "id", Name: "hypotheses_tenant_id_fkey"},
			{Column: "strategy_id", RefTable: "strategies", RefColumn: "id", Name: "hypotheses_strategy_id_fkey"},
			{Column: "channel_id", RefTable: "channels", RefColumn: "id", Name: "hypotheses_channel_id_fkey"},
		}, composite("hypotheses_tenant_id_strategy_id_fkey", "strategies", "strategy_id")...),
			composite("hypotheses_tenant_id_channel_id_fkey", "channels", "channel_id")...),
	}
	return []schemadef.Table{hypotheses, parent("strategies"), parent("channels"), tenants}
}

// TestCompositeFK_EveryColumnFromOneParentRow is the shape the composite
// exists for: on every row, tenant_id is the tenant of the very strategy
// strategy_id names. Recomputed through SeedValue, the values Render emits.
func TestCompositeFK_EveryColumnFromOneParentRow(t *testing.T) {
	p := buildOrFail(t, compositeFKSchema(), Config{Rows: 20, Salt: 1})
	if err := p.Validate(); err != nil {
		t.Fatalf("a composite foreign key is the schema stating the invariant; nothing to refuse: %v", err)
	}
	strategyTenant := map[string]string{}
	for r := 0; r < p.rowsOf["strategies"]; r++ {
		id, _ := p.SeedValue("strategies", "id", r)
		tenant, _ := p.SeedValue("strategies", "tenant_id", r)
		strategyTenant[id] = tenant
	}
	channelTenant := map[string]string{}
	for r := 0; r < p.rowsOf["channels"]; r++ {
		id, _ := p.SeedValue("channels", "id", r)
		tenant, _ := p.SeedValue("channels", "tenant_id", r)
		channelTenant[id] = tenant
	}
	channels := 0
	for r := 0; r < p.rowsOf["hypotheses"]; r++ {
		tenant, _ := p.SeedValue("hypotheses", "tenant_id", r)
		strategy, _ := p.SeedValue("hypotheses", "strategy_id", r)
		owner, ok := strategyTenant[strategy]
		if !ok {
			t.Fatalf("row %d: strategy_id %q is not a seeded strategy — the single-column reference beside the composite is violated", r, strategy)
		}
		if owner != tenant {
			t.Errorf("row %d: tenant_id %q, but strategy %q belongs to tenant %q", r, tenant, strategy, owner)
		}
		if p.SeedIsNull("hypotheses", "channel_id", r) {
			continue
		}
		channel, _ := p.SeedValue("hypotheses", "channel_id", r)
		channels++
		if got, ok := channelTenant[channel]; !ok || got != tenant {
			t.Errorf("row %d: channel %q belongs to tenant %q, the row to %q — the second composite must agree on the column the first placed", r, channel, got, tenant)
		}
	}
	if channels == 0 {
		t.Fatal("no row references a channel — the shared-column case went untested")
	}
}

// TestCompositeFK_EndToEndOnRealPostgres lets postgres judge: introspect the
// applied migration, seed it, and every constraint — the composites and the
// single-column references beside them — holds.
func TestCompositeFK_EndToEndOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(compositeFKMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(compositeFKMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 20, Salt: 1})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := countRows(t, ctx, db, "hypotheses"); n == 0 {
		t.Fatal("no hypotheses seeded — the test proves nothing")
	}
	assertNoCrossTenant(t, ctx, db)
}

func assertNoCrossTenant(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var crossed int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM hypotheses h
		JOIN strategies s ON s.id = h.strategy_id
		WHERE s.tenant_id <> h.tenant_id`).Scan(&crossed); err != nil {
		t.Fatalf("join query: %v", err)
	}
	if crossed != 0 {
		t.Errorf("%d hypotheses name a strategy of another tenant", crossed)
	}
	var parented int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM nodes WHERE parent_id IS NOT NULL`).Scan(&parented); err != nil {
		t.Fatalf("nodes query: %v", err)
	}
	if parented == 0 {
		t.Error("no node has a parent — the self-referencing composite went untested")
	}
}

// selfCompositeSchema is a tenant-scoped tree: nodes (tenant_id, parent_id)
// -> nodes (tenant_id, id), with tenant_id circular (the key references it)
// and its own single-column reference to tenants.
func selfCompositeSchema() []schemadef.Table {
	keys, refs := []string{"tenant_id", "parent_id"}, []string{"tenant_id", "id"}
	nodes := schemadef.Table{
		Name:   "nodes",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			col("id", schemadef.TypeString, true, true),
			col("tenant_id", schemadef.TypeString, true, false),
			col("parent_id", schemadef.TypeString, false, false),
		},
		ForeignKeys: []schemadef.ForeignKey{
			{Column: "tenant_id", RefTable: "tenants", RefColumn: "id", Name: "nodes_tenant_id_fkey"},
			{Column: "tenant_id", RefTable: "nodes", RefColumn: "tenant_id", Name: "nodes_tenant_id_parent_id_fkey", KeyColumns: keys, RefKeyColumns: refs},
			{Column: "parent_id", RefTable: "nodes", RefColumn: "id", Name: "nodes_tenant_id_parent_id_fkey", KeyColumns: keys, RefKeyColumns: refs},
		},
	}
	tenants := schemadef.Table{
		Name:    "tenants",
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{col("id", schemadef.TypeString, true, true)},
	}
	return []schemadef.Table{nodes, tenants}
}

// TestCompositeFK_SelfReferenceStaysInOneParentRow: every parent a node names
// is an earlier node of the node's own tenant, and tenant_id stays a real
// tenant (its single-column reference is not replaced by the circular member).
func TestCompositeFK_SelfReferenceStaysInOneParentRow(t *testing.T) {
	p := buildOrFail(t, selfCompositeSchema(), Config{Rows: 30, Salt: 3})
	tenants := map[string]bool{}
	for r := 0; r < p.rowsOf["tenants"]; r++ {
		id, _ := p.SeedValue("tenants", "id", r)
		tenants[id] = true
	}
	tenantOf, rowOf := map[string]string{}, map[string]int{}
	for r := 0; r < p.rowsOf["nodes"]; r++ {
		id, _ := p.SeedValue("nodes", "id", r)
		tenant, _ := p.SeedValue("nodes", "tenant_id", r)
		if !tenants[tenant] {
			t.Fatalf("row %d: tenant_id %q is not a seeded tenant", r, tenant)
		}
		tenantOf[id], rowOf[id] = tenant, r
	}
	parented := 0
	for r := 0; r < p.rowsOf["nodes"]; r++ {
		if p.SeedIsNull("nodes", "parent_id", r) {
			continue
		}
		parent, _ := p.SeedValue("nodes", "parent_id", r)
		tenant, _ := p.SeedValue("nodes", "tenant_id", r)
		got, ok := tenantOf[parent]
		if !ok {
			t.Fatalf("row %d: parent_id %q is not a seeded node", r, parent)
		}
		if got != tenant {
			t.Errorf("row %d: tenant %q, but parent %q belongs to tenant %q", r, tenant, parent, got)
		}
		if rowOf[parent] >= r {
			t.Errorf("row %d: parent is row %d, not an earlier row", r, rowOf[parent])
		}
		parented++
	}
	if parented == 0 {
		t.Fatal("no node has a parent — the self-referencing composite went untested")
	}
}

// TestCompositeFK_SelfReferenceFormsSameTenantTrees: with as many tenants as
// nodes, an independent tenant pick per node would leave most nodes with no
// earlier node of their tenant. The tree shaping gives every non-root node a
// parent, of its own tenant, across several tenants.
func TestCompositeFK_SelfReferenceFormsSameTenantTrees(t *testing.T) {
	p := buildOrFail(t, selfCompositeSchema(), Config{Rows: 40, Salt: 11})
	if p.rowsOf["tenants"] < p.rowsOf["nodes"] {
		t.Fatalf("fixture wants at least as many tenants (%d) as nodes (%d)", p.rowsOf["tenants"], p.rowsOf["nodes"])
	}
	tree, ok := p.selfTreeComposite("nodes")
	if !ok {
		t.Fatal("nodes' composite self-reference was not shaped into trees")
	}
	tenantOf := map[string]string{}
	for r := 0; r < p.rowsOf["nodes"]; r++ {
		id, _ := p.SeedValue("nodes", "id", r)
		tenantOf[id], _ = p.SeedValue("nodes", "tenant_id", r)
	}
	parented, roots, tenantsWithTrees := 0, 0, map[string]bool{}
	for r := 0; r < p.rowsOf["nodes"]; r++ {
		tenant, _ := p.SeedValue("nodes", "tenant_id", r)
		if p.selfTreeIsRoot("nodes", tree, r) {
			roots++
			if !p.SeedIsNull("nodes", "parent_id", r) {
				t.Errorf("row %d: a root of an optional parent_id names a parent", r)
			}
			continue
		}
		if p.SeedIsNull("nodes", "parent_id", r) {
			t.Errorf("row %d: a non-root node has no parent", r)
			continue
		}
		parent, _ := p.SeedValue("nodes", "parent_id", r)
		if tenantOf[parent] != tenant {
			t.Errorf("row %d: tenant %q, but parent %q belongs to tenant %q", r, tenant, parent, tenantOf[parent])
		}
		parented++
		tenantsWithTrees[tenant] = true
	}
	if parented < p.rowsOf["nodes"]/2 {
		t.Errorf("only %d of %d nodes have a parent", parented, p.rowsOf["nodes"])
	}
	if roots < 2 || len(tenantsWithTrees) < 2 {
		t.Errorf("%d roots, parented nodes in %d tenants: want trees in several tenants", roots, len(tenantsWithTrees))
	}
}

// TestCompositeFK_CompositeUniqueDealsOnlyTheAnchor: a composite UNIQUE over
// a composite's columns deals the unpinned composite's anchor, so the tuple
// never repeats AND every row still reads both columns off one parent; a
// member the composite places but may not be dealt is not dealt. The channel
// composite sorts first, so it is the unpinned one; the strategy composite is
// pinned to it by tenant_id.
func TestCompositeFK_CompositeUniqueDealsOnlyTheAnchor(t *testing.T) {
	tables := compositeFKSchema()
	tables[0].Indexes = append(tables[0].Indexes,
		schemadef.Index{Name: "hypotheses_tenant_channel_key", Columns: []string{"tenant_id", "channel_id"}, Unique: true},
		schemadef.Index{Name: "hypotheses_tenant_strategy_key", Columns: []string{"tenant_id", "strategy_id"}, Unique: true},
	)
	p := buildOrFail(t, tables, Config{Rows: 12, Salt: 5})
	for _, column := range []string{"tenant_id", "strategy_id"} {
		if _, dealt := p.tupleParentRow("hypotheses", column, 1); dealt {
			t.Errorf("%s is placed by a composite foreign key and may not be dealt by a composite UNIQUE", column)
		}
	}
	if _, dealt := p.tupleParentRow("hypotheses", "channel_id", 1); !dealt {
		t.Fatal("channel_id, the unpinned composite's primary-key anchor, was not dealt")
	}
	channelTenant := map[string]string{}
	for r := 0; r < p.rowsOf["channels"]; r++ {
		id, _ := p.SeedValue("channels", "id", r)
		tenant, _ := p.SeedValue("channels", "tenant_id", r)
		channelTenant[id] = tenant
	}
	seen := map[string]int{}
	for r := 0; r < p.rowsOf["hypotheses"]; r++ {
		if p.SeedIsNull("hypotheses", "channel_id", r) {
			t.Fatalf("row %d: a dealt anchor is never NULL", r)
		}
		tenant, _ := p.SeedValue("hypotheses", "tenant_id", r)
		channel, _ := p.SeedValue("hypotheses", "channel_id", r)
		if channelTenant[channel] != tenant {
			t.Errorf("row %d: tenant %q, channel %q of tenant %q", r, tenant, channel, channelTenant[channel])
		}
		k := tupleKey([]string{tenant, channel})
		if prev, dup := seen[k]; dup {
			t.Errorf("rows %d and %d repeat (tenant_id, channel_id) = (%s, %s)", prev, r, tenant, channel)
		}
		seen[k] = r
	}
}

// TestTupleKey_NoSeparatorCollision: values holding any byte cannot make two
// distinct tuples share a key.
func TestTupleKey_NoSeparatorCollision(t *testing.T) {
	pairs := [][2][]string{
		{{"a\x1fb", "c"}, {"a", "b\x1fc"}},
		{{"1:a", ""}, {"1", ":a"}},
		{{""}, {"", ""}},
	}
	for _, pr := range pairs {
		if tupleKey(pr[0]) == tupleKey(pr[1]) {
			t.Errorf("tupleKey(%q) == tupleKey(%q)", pr[0], pr[1])
		}
	}
}

// TestCompositeFK_UniqueOverCompositeColumnAndOrdinaryFK: a composite UNIQUE
// whose members are a column a composite foreign key places (tenant_id, by
// the strategy composite) and an ordinary single-column reference
// (channel_id). The emitted tuples never repeat, or Validate refuses.
func TestCompositeFK_UniqueOverCompositeColumnAndOrdinaryFK(t *testing.T) {
	for _, tc := range []struct {
		name            string
		channelTenanted bool
	}{
		{"channel in a tenant (a diamond through tenant_id)", true},
		{"channel outside any tenant", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tables := compositeFKSchema()
			h := &tables[0]
			// Drop the channel composite: channel_id is an ordinary reference.
			kept := h.ForeignKeys[:0]
			for _, fk := range h.ForeignKeys {
				if fk.Name != "hypotheses_tenant_id_channel_id_fkey" {
					kept = append(kept, fk)
				}
			}
			h.ForeignKeys = kept
			for i := range h.Columns {
				if h.Columns[i].Name == "channel_id" {
					h.Columns[i].NotNull = true
				}
			}
			h.Indexes = append(h.Indexes, schemadef.Index{Name: "hypotheses_tenant_channel_key", Columns: []string{"tenant_id", "channel_id"}, Unique: true})
			if !tc.channelTenanted {
				ch := &tables[2]
				ch.ForeignKeys = nil
				ch.Columns = []schemadef.Column{col("id", schemadef.TypeString, true, true), col("title", schemadef.TypeString, true, false)}
			}
			// Fewer channels and tenants than hypotheses: the tuple cannot
			// be told apart by channel_id alone.
			for _, rows := range []int{3, 12, 40} {
				p := buildOrFail(t, tables, Config{Rows: rows, Salt: 7,
					RowsPerTable: map[string]int{"channels": 4, "tenants": 3, "strategies": 6}})
				if err := p.Validate(); err != nil {
					continue // refusing the plan is an allowed answer
				}
				// tenant_id is the strategy composite's, so the tuple planner
				// may not deal it; channel_id is dealt and the table capped
				// to the channels there are, so channel_id alone keeps the
				// tuple distinct whatever tenant the composite places.
				if _, dealt := p.tupleParentRow("hypotheses", "tenant_id", 1); dealt {
					t.Fatal("tenant_id is placed by the strategy composite and may not be dealt")
				}
				if _, dealt := p.tupleParentRow("hypotheses", "channel_id", 1); !dealt {
					t.Fatal("channel_id, an ordinary reference, was not dealt")
				}
				seen := map[string]int{}
				for r := 0; r < p.rowsOf["hypotheses"]; r++ {
					tenant, _ := p.SeedValue("hypotheses", "tenant_id", r)
					channel, _ := p.SeedValue("hypotheses", "channel_id", r)
					k := tupleKey([]string{tenant, channel})
					if prev, dup := seen[k]; dup {
						t.Errorf("rows=%d: rows %d and %d repeat (tenant_id, channel_id) = (%s, %s)", rows, prev, r, tenant, channel)
					}
					seen[k] = r
				}
			}
		})
	}
}

// uniqueBesideCompositeMigration is the shape of
// TestCompositeFK_UniqueOverCompositeColumnAndOrdinaryFK, for postgres to
// judge: UNIQUE (tenant_id, channel_id), where the strategy composite places
// tenant_id and channel_id is an ordinary reference to fewer rows.
const uniqueBesideCompositeMigration = `
CREATE TABLE tenants (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE strategies (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    title TEXT NOT NULL,
    UNIQUE (tenant_id, id)
);
CREATE TABLE channels (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    title TEXT NOT NULL
);
CREATE TABLE hypotheses (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    strategy_id TEXT NOT NULL REFERENCES strategies(id),
    channel_id TEXT NOT NULL REFERENCES channels(id),
    claim TEXT NOT NULL,
    FOREIGN KEY (tenant_id, strategy_id) REFERENCES strategies (tenant_id, id),
    UNIQUE (tenant_id, channel_id)
);
`

// TestCompositeFK_UniqueBesideCompositeOnRealPostgres: seeded with Apply,
// postgres enforces the UNIQUE, the composite and every single-column key.
func TestCompositeFK_UniqueBesideCompositeOnRealPostgres(t *testing.T) {
	requirePG(t)
	ctx := context.Background()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("pgtest.New: %v", err)
	}
	t.Cleanup(cleanup)
	if _, err := db.Exec(uniqueBesideCompositeMigration); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "00001_init.up.sql"), []byte(uniqueBesideCompositeMigration), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	plan, err := BuildLivePlan(ctx, db, dir, "", Config{Rows: 20, Salt: 3,
		RowsPerTable: map[string]int{"channels": 5, "tenants": 3}})
	if err != nil {
		t.Fatalf("BuildLivePlan: %v", err)
	}
	if _, err := Apply(ctx, db, plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n := countRows(t, ctx, db, "hypotheses"); n < 2 {
		t.Fatalf("%d hypotheses seeded — the UNIQUE went untested", n)
	}
}
