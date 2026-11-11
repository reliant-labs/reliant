package shadowdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

const appliedLadderInit = `
CREATE TABLE owners (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL CHECK (char_length(name) > 0)
);
CREATE TABLE pets (
    id BIGSERIAL PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners(id),
    species TEXT NOT NULL CHECK (species IN ('CAT', 'DOG'))
);
CREATE INDEX pets_owner ON pets (owner_id);
INSERT INTO owners (id, name) VALUES ('o1', 'Ada');
`

// appliedProject is a project directory with one migration, and the shadow
// server every call in the test resolves to.
type appliedProject struct {
	dir    string
	migDir string
	server string
	base   *sql.DB
}

// newAppliedProject points Resolve at the shared test server (through the
// override, so no other candidate is probed) and writes a project whose
// migrations apply there.
func newAppliedProject(t *testing.T) appliedProject {
	t.Helper()
	if testing.Short() {
		t.Skip("applies migrations to a real postgres; skipped under -short")
	}
	scratchURL, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	t.Setenv(pgtest.EnvBaseURL, scratchURL)
	server := Resolve(t.TempDir())
	base, err := sql.Open("postgres", server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })

	dir := t.TempDir()
	migDir := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := appliedProject{dir: dir, migDir: migDir, server: server, base: base}
	p.write(t, "00001_init.up.sql", appliedLadderInit)
	return p
}

func (p appliedProject) write(t *testing.T, name, sql string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p.migDir, name), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fresh is the schema a private apply of the current files produces: what
// every reader must see, run or no run.
func (p appliedProject) fresh(t *testing.T) []schemadef.Table {
	t.Helper()
	tables, err := schemadef.ApplyAndIntrospectAt(p.migDir, p.server)
	if err != nil {
		t.Fatalf("fresh apply: %v", err)
	}
	return tables
}

// scratch counts this process's shadow databases on the server: templates
// and their copies alike.
func (p appliedProject) scratch(t *testing.T) int {
	t.Helper()
	var n int
	pattern := pgtest.ShadowScratchLike()
	if err := p.base.QueryRow(`SELECT count(*) FROM pg_database WHERE datname LIKE $1`, pattern).Scan(&n); err != nil {
		t.Fatalf("count shadow databases: %v", err)
	}
	return n
}

func (p appliedProject) tables(t *testing.T) []schemadef.Table {
	t.Helper()
	tables, err := Tables(p.dir, p.migDir)
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	return tables
}

func (p appliedProject) open(t *testing.T) ([]schemadef.Table, *schemadef.Shadow) {
	t.Helper()
	tables, shadow, err := Open(p.dir, p.migDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(shadow.Close)
	return tables, shadow
}

// The point of the run: however many readers, one migration set is applied
// once, and every reader gets what a private apply would have given it.
func TestRun_EveryReaderSharesOneApply(t *testing.T) {
	p := newAppliedProject(t)
	want := p.fresh(t)
	defer BeginRun()()
	start := CurrentStats()

	var wg sync.WaitGroup
	got := make([][]schemadef.Table, 6)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tables, err := Tables(p.dir, p.migDir)
			if err != nil {
				t.Errorf("Tables %d: %v", i, err)
			}
			got[i] = tables
		}()
	}
	wg.Wait()
	for i, tables := range got {
		if !reflect.DeepEqual(tables, want) {
			t.Fatalf("reader %d got a different schema from a private apply:\n got %+v\nwant %+v", i, tables, want)
		}
	}
	openTables, a := p.open(t)
	_, b := p.open(t)
	if !reflect.DeepEqual(openTables, want) {
		t.Fatalf("Open's tables differ from a private apply")
	}
	introspected, err := schemadef.Introspect(a.DB())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(introspected, want) {
		t.Fatal("an Open copy introspects differently from a private apply")
	}

	if s := StatsSince(start); s.Applies != 1 || s.Reuses != 7 {
		t.Fatalf("8 readers in one run: applied %d×, reused %d× — want one apply and 7 reuses", s.Applies, s.Reuses)
	}

	// Open copies are private: a reader writing to its copy (a rolled-back
	// INSERT still advances a sequence) changes nothing another reads.
	if _, err := a.DB().Exec(`INSERT INTO pets (owner_id, species) VALUES ('o1', 'CAT')`); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err := b.DB().QueryRow(`SELECT nextval('pets_id_seq')`).Scan(&next); err != nil || next != 1 {
		t.Fatalf("copy b's sequence is at %d (err=%v): copy a's write reached it", next, err)
	}
	_, c := p.open(t)
	var rows int
	if err := c.DB().QueryRow(`SELECT count(*) FROM pets`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("a copy opened after a's write sees %d pets (err=%v): the write reached the template", rows, err)
	}
}

// The memo is keyed by the migration bytes, so a run never serves a schema
// its files no longer describe — whatever writes them mid-run.
func TestRun_NeverServesAStaleSchema(t *testing.T) {
	p := newAppliedProject(t)
	defer BeginRun()()
	start := CurrentStats()
	before := p.tables(t)

	p.write(t, "00002_more.up.sql", `ALTER TABLE owners ADD COLUMN email TEXT;`)
	after := p.tables(t)
	if reflect.DeepEqual(after, before) {
		t.Fatal("a migration added mid-run was not seen: the run served the schema from before it")
	}
	if !reflect.DeepEqual(after, p.fresh(t)) {
		t.Fatal("the schema after a mid-run edit differs from a private apply of the edited files")
	}
	_, shadow := p.open(t)
	if _, err := shadow.DB().Exec(`SELECT email FROM owners`); err != nil {
		t.Fatalf("an Open copy after the edit lacks the new column: %v", err)
	}

	// Content, not history: back to the original bytes is back to the
	// original template, with no third apply.
	if err := os.Remove(filepath.Join(p.migDir, "00002_more.up.sql")); err != nil {
		t.Fatal(err)
	}
	if reverted := p.tables(t); !reflect.DeepEqual(reverted, before) {
		t.Fatal("reverting the edit did not restore the original schema")
	}
	if s := StatsSince(start); s.Applies != 2 {
		t.Fatalf("two distinct migration sets applied %d× — want 2", s.Applies)
	}
}

// Templates live exactly as long as the outermost run: nested runs share
// them, and the end of the last one drops every database the run made.
func TestRun_EndDropsEveryTemplate(t *testing.T) {
	p := newAppliedProject(t)
	before := p.scratch(t)

	endOuter := BeginRun()
	start := CurrentStats()
	endInner := BeginRun()
	_, shadow, err := Open(p.dir, p.migDir)
	if err != nil {
		t.Fatal(err)
	}
	shadow.Close()
	endInner()
	endInner() // idempotent: must not end the outer run
	p.tables(t)
	if s := StatsSince(start); s.Applies != 1 || s.Reuses != 1 {
		t.Fatalf("after a nested run ended: applied %d×, reused %d× — the outer run must keep its template", s.Applies, s.Reuses)
	}
	if p.scratch(t) == before {
		t.Fatal("precondition: the open run holds a template database")
	}
	endOuter()
	if after := p.scratch(t); after != before {
		t.Fatalf("shadow databases: %d before the run, %d after it ended — a template was not dropped", before, after)
	}

	// Outside a run nothing is kept: each call is a private apply.
	start = CurrentStats()
	p.tables(t)
	p.tables(t)
	if s := StatsSince(start); s.Applies != 0 || s.Reuses != 0 {
		t.Fatalf("outside a run the memo was used: %+v", s)
	}
	if after := p.scratch(t); after != before {
		t.Fatalf("shadow databases: %d before, %d after two calls outside a run", before, after)
	}
}

// A failed apply is not remembered: the reader after the fix gets the fixed
// schema in the same run.
func TestRun_FailedApplyIsNotMemoized(t *testing.T) {
	p := newAppliedProject(t)
	defer BeginRun()()
	p.write(t, "00002_bad.up.sql", `ALTER TABLE nowhere ADD COLUMN x TEXT;`)
	if _, err := Tables(p.dir, p.migDir); err == nil {
		t.Fatal("a migration postgres rejects must fail Tables")
	}
	if _, _, err := Open(p.dir, p.migDir); err == nil {
		t.Fatal("a migration postgres rejects must fail Open")
	}
	p.write(t, "00002_bad.up.sql", `ALTER TABLE owners ADD COLUMN x TEXT;`)
	if got := p.tables(t); !reflect.DeepEqual(got, p.fresh(t)) {
		t.Fatal("after the fix, the run served something other than the fixed schema")
	}
}

// One run, one server: the shadow server is resolved once per project, so a
// candidate that appears or vanishes mid-run cannot split the run's readers
// across two postgres servers.
func TestRun_ResolvesTheServerOnce(t *testing.T) {
	p := newAppliedProject(t)
	defer BeginRun()()
	first := p.tables(t)
	// Every later resolution would now fail loudly: the override is honored
	// unprobed.
	t.Setenv(pgtest.EnvBaseURL, "postgres://nobody:nothing@127.0.0.1:1/postgres?sslmode=disable")
	if got := p.tables(t); !reflect.DeepEqual(got, first) {
		t.Fatal("the run re-resolved its shadow server mid-run")
	}
	_, shadow := p.open(t)
	if shadow == nil {
		t.Fatal("Open must reuse the run's server too")
	}
}

// A project with no migrations has no schema, and asks postgres nothing —
// in a run or out of one. The override here points at a closed port, so any
// connection attempt would fail the test.
func TestTables_NoMigrationsNeedsNoServer(t *testing.T) {
	t.Setenv(pgtest.EnvBaseURL, "postgres://nobody:nothing@127.0.0.1:1/postgres?sslmode=disable")
	dir := t.TempDir()
	migDir := filepath.Join(dir, "db", "migrations")
	check := func(label string) {
		t.Helper()
		tables, err := Tables(dir, migDir)
		if err != nil || tables != nil {
			t.Errorf("%s: Tables = %v, %v; want nil, nil", label, tables, err)
		}
		tables, shadow, err := Open(dir, migDir)
		if err != nil || tables != nil || shadow != nil {
			t.Errorf("%s: Open = %v, %v, %v; want nils", label, tables, shadow, err)
		}
	}
	check("outside a run")
	end := BeginRun()
	defer end()
	check("inside a run")
	if err := os.MkdirAll(migDir, 0o755); err != nil {
		t.Fatal(err)
	}
	check("inside a run, empty directory")
}
