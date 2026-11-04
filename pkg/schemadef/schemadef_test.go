package schemadef

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

func writeMig(t *testing.T, dir, name, sql string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requireRealPG skips a test under -short: ApplyAndIntrospect boots a
// real ephemeral postgres (pkg/pgtest), which is well over the 2s short
// budget (and downloads the binary on a fresh machine). The full mode /
// CI gate still exercises these.
func requireRealPG(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("ApplyAndIntrospect boots real postgres; skipped under -short")
	}
}

func TestApplyAndIntrospect_PostgresFlavoredDDL(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_bookmarks.up.sql", `
CREATE TABLE bookmarks (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    url TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    tags TEXT[] NOT NULL DEFAULT '{}',
    visits BIGINT NOT NULL DEFAULT 0,
    score DOUBLE PRECISION NOT NULL DEFAULT 0,
    done BOOLEAN NOT NULL DEFAULT FALSE,
    meta JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX idx_bookmarks_url ON bookmarks (url);
`)
	// Down files must be ignored.
	writeMig(t, dir, "00001_create_bookmarks.down.sql", `DROP TABLE bookmarks;`)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "bookmarks" {
		t.Fatalf("tables = %+v, want exactly [bookmarks]", tables)
	}
	bt := tables[0]

	wantTypes := map[string]struct {
		typ     CanonicalType
		isArray bool
		notNull bool
	}{
		"id":         {TypeString, false, true},
		"url":        {TypeString, false, true},
		"title":      {TypeString, false, true},
		"tags":       {TypeString, true, true},
		"visits":     {TypeInt, false, true},
		"score":      {TypeFloat, false, true},
		"done":       {TypeBool, false, true},
		"meta":       {TypeJSON, false, true},
		"created_at": {TypeTime, false, true},
		"updated_at": {TypeTime, false, true},
		"deleted_at": {TypeTime, false, false},
	}
	if len(bt.Columns) != len(wantTypes) {
		t.Fatalf("got %d columns, want %d: %+v", len(bt.Columns), len(wantTypes), bt.Columns)
	}
	for _, col := range bt.Columns {
		w, ok := wantTypes[col.Name]
		if !ok {
			t.Errorf("unexpected column %q", col.Name)
			continue
		}
		if col.Type != w.typ || col.IsArray != w.isArray || col.NotNull != w.notNull {
			t.Errorf("column %s = {type:%s array:%v notnull:%v}, want %+v", col.Name, col.Type, col.IsArray, col.NotNull, w)
		}
	}
	if len(bt.PKCols) != 1 || bt.PKCols[0] != "id" {
		t.Errorf("PKCols = %v, want [id]", bt.PKCols)
	}
	if len(bt.Indexes) != 1 || bt.Indexes[0].Name != "idx_bookmarks_url" || bt.Indexes[0].Columns[0] != "url" {
		t.Errorf("Indexes = %+v, want idx_bookmarks_url(url)", bt.Indexes)
	}

	conv := DetectConventions(bt)
	if !conv.SoftDelete || !conv.Timestamps {
		t.Errorf("conventions = %+v, want SoftDelete+Timestamps", conv)
	}
	wantSearch := []string{"url", "title"}
	if len(conv.SearchColumns) != 2 || conv.SearchColumns[0] != wantSearch[0] || conv.SearchColumns[1] != wantSearch[1] {
		t.Errorf("SearchColumns = %v, want %v", conv.SearchColumns, wantSearch)
	}
}

func TestApplyAndIntrospect_MigrationLadderWithDataMovement(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_people.up.sql", `
CREATE TABLE people (
    id TEXT PRIMARY KEY CHECK (id <> ''),
    name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT (now()),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (now())
);
`)
	// Second migration: add columns + data movement splitting a column,
	// then drop the old column. The UPDATE is native postgres SQL and
	// executes verbatim on the real-pg shadow.
	writeMig(t, dir, "00002_split_name.up.sql", `
ALTER TABLE people ADD COLUMN first_name TEXT NOT NULL DEFAULT '';
ALTER TABLE people ADD COLUMN last_name TEXT NOT NULL DEFAULT '';
UPDATE people SET first_name = split_part(name, ' ', 1),
                  last_name  = split_part(name, ' ', 2);
ALTER TABLE people DROP COLUMN name;
`)
	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	cols := map[string]bool{}
	for _, c := range tables[0].Columns {
		cols[c.Name] = true
	}
	if cols["name"] {
		t.Error("dropped column `name` still present — ALTER TABLE DROP COLUMN not applied")
	}
	if !cols["first_name"] || !cols["last_name"] {
		t.Errorf("added columns missing: %v", cols)
	}
}

func TestApplyAndIntrospect_SkipsNonSchemaPostgresisms(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", `
CREATE TABLE things (
    id TEXT PRIMARY KEY,
    payload JSONB NOT NULL DEFAULT '{}'
);
-- Postgres-only auxiliary DDL: must be skipped, not fatal.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
COMMENT ON TABLE things IS 'a comment';
CREATE OR REPLACE FUNCTION touch_updated() RETURNS trigger AS $fn$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$fn$ LANGUAGE plpgsql;
-- Postgres-only DML that the shadow can't run: skipped.
INSERT INTO things (id, payload) VALUES ('x', '{"a":1}'::jsonb);
`)
	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect should skip non-schema postgresisms: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "things" {
		t.Fatalf("tables = %+v", tables)
	}
}

func TestApplyAndIntrospect_FailsLoudOnBrokenTableDDL(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	// A CREATE TABLE that real postgres rejects (here: an EXCLUDE gist
	// constraint with no gist opclass for text `&&`) is a hard error —
	// the table that defines part of the schema must not be silently
	// skipped, or the generated ORM would lie about what columns exist.
	writeMig(t, dir, "00001_bad.up.sql", `
CREATE TABLE broken (
    id TEXT PRIMARY KEY,
    span TEXT,
    CONSTRAINT no_overlap EXCLUDE USING gist (span WITH &&)
);
`)
	if _, err := ApplyAndIntrospect(dir); err == nil {
		t.Fatal("a CREATE TABLE postgres rejects must be a hard error, not a silent skip")
	}
}

// OpenShadowAt's error contract is the ordinary one: on error there is
// nothing to close. A replay failure therefore returns NO shadow and has
// already dropped the scratch database it created — the caller checks err
// and only then defers Close. Returning the live shadow beside the error (the
// old contract) leaked the scratch database into any caller that wrote the
// idiomatic err-check-first form.
func TestOpenShadowAt_ReplayErrorLeavesNothingOpen(t *testing.T) {
	requireRealPG(t)
	baseURL, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("pgtest.NewURL: %v", err)
	}
	t.Cleanup(cleanup)
	base, err := sql.Open("postgres", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	scratch := func() int {
		t.Helper()
		var n int
		pattern := pgtest.ShadowScratchLike()
		if err := base.QueryRow(`SELECT count(*) FROM pg_database WHERE datname LIKE $1`, pattern).Scan(&n); err != nil {
			t.Fatalf("count scratch databases: %v", err)
		}
		return n
	}

	dir := t.TempDir()
	writeMig(t, dir, "00001_bad.up.sql", `
CREATE TABLE broken (
    id TEXT PRIMARY KEY,
    span TEXT,
    CONSTRAINT no_overlap EXCLUDE USING gist (span WITH &&)
);
`)
	before := scratch()
	shadow, err := OpenShadowAt(dir, baseURL)
	if err == nil {
		shadow.Close()
		t.Fatal("a migration postgres rejects must fail OpenShadowAt")
	}
	if shadow != nil {
		shadow.Close()
		t.Error("OpenShadowAt returned a live shadow beside its error; on error there must be nothing to close")
	}
	if after := scratch(); after != before {
		t.Errorf("scratch databases: %d before, %d after a failed OpenShadowAt — the shadow it opened was not dropped", before, after)
	}
}

func TestApplyAndIntrospect_EmptyOrMissingDir(t *testing.T) {
	tables, err := ApplyAndIntrospect(filepath.Join(t.TempDir(), "nope"))
	if err != nil || tables != nil {
		t.Fatalf("missing dir: got (%v, %v), want (nil, nil)", tables, err)
	}
}

// The column markers are declared as plain postgres catalog comments and
// read back through the catalog, so nothing downstream — the ORM's write
// policy, the optimistic-concurrency predicate — exists unless the COMMENT
// survives the round trip. This is that link, asserted against real
// postgres rather than against migration text.
func TestApplyAndIntrospect_ColumnMarkersSurviveTheCatalog(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_crews.up.sql", `
CREATE TABLE crews (
    id TEXT PRIMARY KEY,
    company_id TEXT NOT NULL,
    name TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 0
);
COMMENT ON COLUMN crews.company_id IS 'forge:immutable';
COMMENT ON COLUMN crews.version IS 'forge:version';
`)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("tables = %+v, want exactly [crews]", tables)
	}

	byName := map[string]Column{}
	for _, c := range tables[0].Columns {
		byName[c.Name] = c
	}
	if !byName["version"].HasMarker(ColumnMarkerVersion) {
		t.Errorf("crews.version lost its forge:version marker; comment = %q", byName["version"].Comment)
	}
	if !byName["company_id"].HasMarker(ColumnMarkerImmutable) {
		t.Errorf("crews.company_id lost its forge:immutable marker; comment = %q", byName["company_id"].Comment)
	}
	// The markers are per-column facts, not table-wide ones: an undeclared
	// column must carry neither, or every column would opt into everything.
	if byName["name"].HasMarker(ColumnMarkerVersion) || byName["name"].HasMarker(ColumnMarkerImmutable) {
		t.Errorf("crews.name carries a marker it never declared; comment = %q", byName["name"].Comment)
	}
	// The two markers are distinct: forge:version must not read as
	// forge:immutable or the ORM would silently apply the wrong policy.
	if byName["version"].HasMarker(ColumnMarkerImmutable) {
		t.Error("forge:version must not satisfy the forge:immutable marker check")
	}
}

func TestSplitStatements(t *testing.T) {
	got := SplitStatements(`
BEGIN;
CREATE TABLE a (x TEXT DEFAULT 'semi;colon');
CREATE TRIGGER trg AFTER INSERT ON a BEGIN UPDATE a SET x = 'y'; END;
COMMIT;
`)
	if len(got) != 4 {
		t.Fatalf("got %d statements, want 4:\n%q", len(got), got)
	}
}

func TestMapDeclaredType(t *testing.T) {
	cases := []struct {
		decl  string
		want  CanonicalType
		array bool
		known bool
	}{
		{"TEXT", TypeString, false, true},
		{"varchar(255)", TypeString, false, true},
		{"UUID", TypeString, false, true},
		{"BIGSERIAL", TypeInt, false, true},
		{"DOUBLE PRECISION", TypeFloat, false, true},
		{"NUMERIC(10,2)", TypeFloat, false, true},
		{"TIMESTAMPTZ", TypeTime, false, true},
		{"timestamp with time zone", TypeTime, false, true},
		{"JSONB", TypeJSON, false, true},
		{"BYTEA", TypeBytes, false, true},
		{"TEXT[]", TypeString, true, true},
		{"BIGINT[]", TypeInt, true, true},

		// Every one of these falls back to TypeString, and the fallback is
		// the reason `known` exists: postgres accepts none of them as
		// arbitrary text, so a caller that cannot tell them from a real
		// TEXT column projects a wire contract the database will reject.
		{"mood_enum", TypeString, false, false}, // native pg enum
		{"MONEY", TypeString, false, false},
		{"INET", TypeString, false, false},
		{"INTERVAL", TypeString, false, false},
		{"TSVECTOR", TypeString, false, false},
		{"mood_enum[]", TypeString, true, false},
		{"", TypeString, false, false}, // empty/untyped
	}
	for _, c := range cases {
		got, arr, known := MapDeclaredType(c.decl)
		if got != c.want || arr != c.array || known != c.known {
			t.Errorf("MapDeclaredType(%q) = (%s,%v,known=%v), want (%s,%v,known=%v)",
				c.decl, got, arr, known, c.want, c.array, c.known)
		}
	}
}

func TestApplyAndIntrospect_CommentPrefixedDDLIsNotSilentlySkipped(t *testing.T) {
	// The kalshi-parity audit found banner comments attaching to the
	// following statement, which defeated isSchemaDefining and let a
	// FAILING CREATE TABLE be skipped silently — a partial schema with
	// no error. Comment-prefixed broken DDL must hard-error.
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_bad.up.sql", `
-- ----------------------------------------------------------------
-- forecasts: banner comment style
-- ----------------------------------------------------------------
CREATE TABLE forecasts (
    id TEXT PRIMARY KEY,
    payload JSONB NOT NULL DEFAULT 'oops'::jsonb -- 'oops' is invalid json → postgres rejects the table
);
`)
	if _, err := ApplyAndIntrospect(dir); err == nil {
		t.Fatal("comment-prefixed failing CREATE TABLE must be a hard error, not a silent skip")
	}
}

func TestApplyAndIntrospect_PostgresIdiomsApplyVerbatim(t *testing.T) {
	// The recurring idioms from real postgres projects apply VERBATIM on
	// the real-pg shadow — no normalization: unparenthesized function
	// defaults, literal ::type casts, ADD COLUMN IF NOT EXISTS, multi-ADD
	// ALTERs all run as written.
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", `
CREATE TABLE markets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    meta JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`)
	writeMig(t, dir, "00002_evolve.up.sql", `
-- add weights (idempotent in pg)
ALTER TABLE markets
    ADD COLUMN IF NOT EXISTS model_weights JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE markets ADD COLUMN a TEXT NOT NULL DEFAULT '', ADD COLUMN b TEXT NOT NULL DEFAULT '';
-- re-adding the same column: pg's IF NOT EXISTS makes this a no-op
ALTER TABLE markets ADD COLUMN IF NOT EXISTS a TEXT NOT NULL DEFAULT '';
`)
	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("postgres idioms must apply verbatim on the real-pg shadow: %v", err)
	}
	cols := map[string]Column{}
	for _, c := range tables[0].Columns {
		cols[c.Name] = c
	}
	for _, want := range []string{"id", "meta", "created_at", "model_weights", "a", "b"} {
		if _, ok := cols[want]; !ok {
			t.Errorf("column %q missing after normalization: have %v", want, tables[0].Columns)
		}
	}
	if got := cols["id"].Type; got != TypeString {
		t.Errorf("UUID id type = %s, want string", got)
	}
	if got := cols["created_at"].Type; got != TypeTime {
		t.Errorf("created_at type = %s, want time", got)
	}
}

// TestDetectConventions_TimestampTypesAware pins the type-awareness of
// the managed-timestamps convention (M3, kalshi fr-3fba9166ba):
// created_at/updated_at count as managed only when the generator can
// actually stamp them — time columns (TIMESTAMPTZ et al) or legacy TEXT
// columns (stamped as RFC3339Nano strings). Exotic types (epoch
// INTEGER) opt the table out of managed timestamps instead of producing
// stamping code that cannot compile or would corrupt data. deleted_at
// was already type-gated the same way.
func TestDetectConventions_TimestampTypesAware(t *testing.T) {
	col := func(name string, typ CanonicalType) Column {
		return Column{Name: name, Type: typ, NotNull: true}
	}
	cases := []struct {
		name    string
		created CanonicalType
		updated CanonicalType
		want    bool
	}{
		{"time pair", TypeTime, TypeTime, true},
		{"legacy TEXT pair", TypeString, TypeString, true},
		{"mixed time + TEXT", TypeTime, TypeString, true},
		{"epoch integers", TypeInt, TypeInt, false},
		{"one unstampable", TypeTime, TypeInt, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tbl := Table{
				Name: "things",
				Columns: []Column{
					{Name: "id", Type: TypeString, IsPK: true, NotNull: true},
					col("created_at", tc.created),
					col("updated_at", tc.updated),
				},
			}
			if got := DetectConventions(tbl).Timestamps; got != tc.want {
				t.Errorf("Timestamps = %v, want %v (created=%s updated=%s)", got, tc.want, tc.created, tc.updated)
			}
		})
	}

	// Array-typed managed columns are never stampable.
	tbl := Table{
		Name: "things",
		Columns: []Column{
			{Name: "id", Type: TypeString, IsPK: true, NotNull: true},
			{Name: "created_at", Type: TypeString, IsArray: true, NotNull: true},
			{Name: "updated_at", Type: TypeTime, NotNull: true},
		},
	}
	if DetectConventions(tbl).Timestamps {
		t.Error("array-typed created_at must not count as a managed timestamp")
	}
}

// TestApplyAndIntrospect_SchemaQualifiedDDL is the regression for the bug
// class that froze cp-forge's ORM: a migration that creates its tables in
// a NON-public schema (CREATE TABLE controlplane.foo ...). The old
// in-memory SQLite shadow could not parse the schema-qualified name and
// silently produced an empty schema (or errored), so the generated ORM
// went stale. Real postgres handles it natively; introspection enumerates
// user tables across every schema and keys them by bare table name.
func TestApplyAndIntrospect_SchemaQualifiedDDL(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_controlplane.up.sql", `
CREATE SCHEMA IF NOT EXISTS controlplane;

CREATE TABLE controlplane.deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    region TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    labels TEXT[] NOT NULL DEFAULT '{}'::text[],
    spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX idx_deployments_region ON controlplane.deployments (region);
`)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("schema-qualified DDL must introspect cleanly on real pg: %v", err)
	}
	if len(tables) != 1 || tables[0].Name != "deployments" {
		t.Fatalf("tables = %+v, want exactly [deployments] (keyed by bare name)", tables)
	}
	dt := tables[0]

	want := map[string]struct {
		typ     CanonicalType
		isArray bool
	}{
		"id": {TypeString, false}, "region": {TypeString, false},
		"name": {TypeString, false}, "labels": {TypeString, true},
		"spec": {TypeJSON, false}, "created_at": {TypeTime, false},
		"updated_at": {TypeTime, false}, "deleted_at": {TypeTime, false},
	}
	if len(dt.Columns) != len(want) {
		t.Fatalf("got %d columns, want %d: %+v", len(dt.Columns), len(want), dt.Columns)
	}
	for _, c := range dt.Columns {
		w, ok := want[c.Name]
		if !ok {
			t.Errorf("unexpected column %q", c.Name)
			continue
		}
		if c.Type != w.typ || c.IsArray != w.isArray {
			t.Errorf("column %s = {%s array:%v}, want {%s array:%v}", c.Name, c.Type, c.IsArray, w.typ, w.isArray)
		}
	}
	if len(dt.PKCols) != 1 || dt.PKCols[0] != "id" {
		t.Errorf("PKCols = %v, want [id]", dt.PKCols)
	}
	if len(dt.Indexes) != 1 || dt.Indexes[0].Name != "idx_deployments_region" {
		t.Errorf("Indexes = %+v, want idx_deployments_region", dt.Indexes)
	}
	conv := DetectConventions(dt)
	if !conv.SoftDelete || !conv.Timestamps {
		t.Errorf("conventions = %+v, want SoftDelete+Timestamps", conv)
	}
}

// TestApplyAndIntrospect_GeneratedColumn proves a GENERATED ALWAYS AS
// (...) STORED column is flagged IsGenerated (it drives the ORM's
// ,scanonly tag so the generated code never writes a DB-computed column).
func TestApplyAndIntrospect_GeneratedColumn(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_create_people.up.sql", `
CREATE TABLE people (
    id BIGSERIAL PRIMARY KEY,
    first_name TEXT NOT NULL DEFAULT '',
    last_name TEXT NOT NULL DEFAULT '',
    full_name TEXT GENERATED ALWAYS AS (first_name || ' ' || last_name) STORED
);
`)

	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("got %d tables, want 1", len(tables))
	}
	byName := map[string]Column{}
	for _, c := range tables[0].Columns {
		byName[c.Name] = c
	}
	if !byName["full_name"].IsGenerated {
		t.Errorf("full_name should be IsGenerated")
	}
	if byName["first_name"].IsGenerated {
		t.Errorf("first_name (plain column) must not be IsGenerated")
	}
}

// TestApplyAndIntrospect_KeyAllocation pins DatabaseSupplied, the fact the
// ORM's ,autoincrement is projected from: a serial (nextval DEFAULT), an
// identity column (which the catalog reports with NO default) and a uuid
// DEFAULT are the database's to fill; `integer PRIMARY KEY` with no default
// is the caller's (reliant's legacy_credential_copy).
func TestApplyAndIntrospect_KeyAllocation(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_keys.up.sql", `
CREATE TABLE caller_keyed (id integer PRIMARY KEY CHECK (id = 1));
CREATE TABLE serial_keyed (id serial PRIMARY KEY);
CREATE TABLE identity_keyed (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY);
CREATE TABLE by_default_keyed (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY);
CREATE TABLE uuid_keyed (id uuid PRIMARY KEY DEFAULT gen_random_uuid());
`)
	tables, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	want := map[string]struct{ identity, supplied bool }{
		"caller_keyed":     {false, false},
		"serial_keyed":     {false, true},
		"identity_keyed":   {true, true},
		"by_default_keyed": {true, true},
		"uuid_keyed":       {false, true},
	}
	for _, tbl := range tables {
		w, ok := want[tbl.Name]
		if !ok {
			continue
		}
		delete(want, tbl.Name)
		id := tbl.Columns[0]
		if id.IsIdentity != w.identity || id.DatabaseSupplied() != w.supplied {
			t.Errorf("%s.id: IsIdentity=%v DatabaseSupplied=%v (default %q), want %v / %v",
				tbl.Name, id.IsIdentity, id.DatabaseSupplied(), id.Default, w.identity, w.supplied)
		}
	}
	for name := range want {
		t.Errorf("table %s was not introspected", name)
	}
}
