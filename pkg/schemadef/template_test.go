package schemadef

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// templateLadder is a migration ladder that exercises every part of the
// model a Template serves: two schemas, foreign keys with comments and
// on-delete actions, CHECKs, partial and expression indexes, generated
// columns, column and table comments, a trigger, data movement, and a
// statement the bare shadow refuses and the policy skips.
var templateLadder = map[string]string{
	"00001_init.up.sql": `
CREATE SCHEMA IF NOT EXISTS billing;
CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'CLOSED')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX accounts_email_live ON accounts (lower(email)) WHERE deleted_at IS NULL;
COMMENT ON TABLE accounts IS 'forge:append-only';
COMMENT ON COLUMN accounts.email IS 'forge:immutable';
`,
	"00002_billing.up.sql": `
CREATE TABLE billing.invoices (
    id BIGSERIAL PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    amount_cents BIGINT NOT NULL CHECK (amount_cents >= 0),
    tax_cents BIGINT GENERATED ALWAYS AS (amount_cents / 10) STORED,
    tags TEXT[] NOT NULL DEFAULT '{}'::text[],
    meta JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX invoices_account ON billing.invoices (account_id, amount_cents);
COMMENT ON CONSTRAINT invoices_account_id_fkey ON billing.invoices IS 'forge:ref authoritative';
INSERT INTO accounts (id, email) VALUES ('seed', 'seed@example.com');
CREATE EXTENSION IF NOT EXISTS this_extension_does_not_exist;
`,
	"00003_trigger.up.sql": `
CREATE FUNCTION touch() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER accounts_touch BEFORE UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION touch();
ALTER TABLE accounts ADD COLUMN nickname VARCHAR(12);
`,
}

func writeLadder(t *testing.T, ladder map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, sql := range ladder {
		writeMig(t, dir, name, sql)
	}
	return dir
}

func readSet(t *testing.T, dir string) *MigrationSet {
	t.Helper()
	set, err := ReadMigrationSet(dir)
	if err != nil {
		t.Fatalf("ReadMigrationSet(%s): %v", dir, err)
	}
	return set
}

// The digest is a name for the bytes a shadow would apply, in the order it
// would apply them: equal exactly when those are equal.
func TestMigrationSet_DigestNamesTheAppliedBytes(t *testing.T) {
	base := readSet(t, writeLadder(t, templateLadder))
	if base.Len() != len(templateLadder) {
		t.Fatalf("Len = %d, want %d", base.Len(), len(templateLadder))
	}
	if again := readSet(t, writeLadder(t, templateLadder)); again.Digest() != base.Digest() {
		t.Error("the same files in another directory must have the same digest: the digest names content, not a path")
	}

	mutate := func(name string, edit func(dir string)) {
		t.Helper()
		dir := writeLadder(t, templateLadder)
		edit(dir)
		if readSet(t, dir).Digest() == base.Digest() {
			t.Errorf("%s: digest unchanged — a schema built from different bytes would be served as this one", name)
		}
	}
	mutate("one byte of one file", func(dir string) {
		writeMig(t, dir, "00003_trigger.up.sql", templateLadder["00003_trigger.up.sql"]+" ")
	})
	mutate("a renamed file (apply order moves)", func(dir string) {
		if err := os.Rename(filepath.Join(dir, "00001_init.up.sql"), filepath.Join(dir, "00004_init.up.sql")); err != nil {
			t.Fatal(err)
		}
	})
	mutate("an added migration", func(dir string) {
		writeMig(t, dir, "00004_more.up.sql", "CREATE TABLE more (id TEXT PRIMARY KEY);")
	})
	mutate("a removed migration", func(dir string) {
		if err := os.Remove(filepath.Join(dir, "00002_billing.up.sql")); err != nil {
			t.Fatal(err)
		}
	})
	mutate("content moved between two files", func(dir string) {
		writeMig(t, dir, "00001_init.up.sql", templateLadder["00001_init.up.sql"]+templateLadder["00002_billing.up.sql"])
		writeMig(t, dir, "00002_billing.up.sql", "")
	})

	// What a shadow never applies does not name the schema.
	dir := writeLadder(t, templateLadder)
	writeMig(t, dir, "00002_billing.down.sql", "DROP TABLE billing.invoices;")
	writeMig(t, dir, "README.md", "notes")
	if err := os.Mkdir(filepath.Join(dir, "sub.up.sql"), 0o755); err != nil {
		t.Fatal(err)
	}
	if readSet(t, dir).Digest() != base.Digest() {
		t.Error("files a shadow never applies (down migrations, docs, directories) changed the digest")
	}
}

func TestMigrationSet_MissingDirIsEmpty(t *testing.T) {
	set := readSet(t, filepath.Join(t.TempDir(), "nope"))
	if set.Len() != 0 {
		t.Fatalf("Len = %d, want 0", set.Len())
	}
	if _, err := set.ApplyTemplateAt(""); err == nil {
		t.Error("an empty set must refuse to build a template: a project with no migrations needs no postgres")
	}
}

// TestCloneTables_SharesNothing fills every field of the model by reflection,
// so a slice, map or pointer field added to Table (or anything it holds)
// without a matching line in cloneTables fails here instead of letting one
// reader's edit leak into the next reader's schema.
func TestCloneTables_SharesNothing(t *testing.T) {
	orig := make([]Table, 2)
	for i := range orig {
		fillForTest(t, reflect.ValueOf(&orig[i]).Elem(), "Table")
	}
	cp := cloneTables(orig)
	if !reflect.DeepEqual(cp, orig) {
		t.Fatalf("clone differs from the original:\n got %+v\nwant %+v", cp, orig)
	}
	assertDisjoint(t, reflect.ValueOf(cp), reflect.ValueOf(orig), "[]Table")
	if cloneTables(nil) != nil {
		t.Error("cloneTables(nil) must stay nil")
	}
}

// fillForTest sets every field reachable from v to a non-zero value, with
// one element in every slice.
func fillForTest(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				t.Fatalf("%s.%s is unexported: cloneTables and this test only know exported fields", path, f.Name)
			}
			fillForTest(t, v.Field(i), path+"."+f.Name)
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillForTest(t, s.Index(0), path+"[0]")
		v.Set(s)
	default:
		t.Fatalf("%s has kind %s: teach cloneTables (and this test) to copy it", path, v.Kind())
	}
}

// assertDisjoint fails when a and b share the backing array of any slice.
func assertDisjoint(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	switch a.Kind() {
	case reflect.Slice:
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			t.Errorf("%s shares its backing array with the original", path)
		}
		for i := 0; i < a.Len(); i++ {
			assertDisjoint(t, a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			assertDisjoint(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
		}
	}
}

// countScratch counts this process's NewAtURL scratch databases.
func countScratch(t *testing.T, base *sql.DB) int {
	t.Helper()
	var n int
	pattern := pgtest.ShadowScratchLike()
	if err := base.QueryRow(`SELECT count(*) FROM pg_database WHERE datname LIKE $1`, pattern).Scan(&n); err != nil {
		t.Fatalf("count scratch databases: %v", err)
	}
	return n
}

// scratchServer is a server URL for NewAtURL-style shadows plus an open
// connection to count what they leave behind.
func scratchServer(t *testing.T) (string, *sql.DB) {
	t.Helper()
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
	return baseURL, base
}

// A template serves exactly the model a fresh apply produces, on both kinds
// of server, and a copy of it introspects to that model too.
func TestTemplate_ServesWhatAFreshApplyProduces(t *testing.T) {
	requireRealPG(t)
	dir := writeLadder(t, templateLadder)
	want, err := ApplyAndIntrospect(dir)
	if err != nil {
		t.Fatalf("ApplyAndIntrospect: %v", err)
	}
	if len(want) != 2 {
		t.Fatalf("precondition: the ladder has 2 tables, introspected %d", len(want))
	}
	baseURL, _ := scratchServer(t)
	for name, server := range map[string]string{"shared server": "", "configured server": baseURL} {
		t.Run(name, func(t *testing.T) {
			tpl, err := readSet(t, dir).ApplyTemplateAt(server)
			if err != nil {
				t.Fatalf("ApplyTemplateAt: %v", err)
			}
			t.Cleanup(tpl.Close)
			if got := tpl.Tables(); !reflect.DeepEqual(got, want) {
				t.Fatalf("template tables differ from a fresh apply:\n got %+v\nwant %+v", got, want)
			}
			if tpl.Digest() != readSet(t, dir).Digest() {
				t.Error("the template's digest must be its migration set's")
			}
			shadow, err := tpl.Open()
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer shadow.Close()
			got, err := Introspect(shadow.DB())
			if err != nil {
				t.Fatalf("Introspect(copy): %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("a copy of the template introspects differently from a fresh apply:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// Tables hands every caller its own copy: one reader trimming its model
// cannot change the next reader's.
func TestTemplate_TablesAreTheCallersOwn(t *testing.T) {
	requireRealPG(t)
	tpl, err := readSet(t, writeLadder(t, templateLadder)).ApplyTemplateAt("")
	if err != nil {
		t.Fatalf("ApplyTemplateAt: %v", err)
	}
	defer tpl.Close()
	first := tpl.Tables()
	first[0].Name = "mutated"
	first[0].Columns[0].Name = "mutated"
	first[0].Indexes = nil
	second := tpl.Tables()
	if second[0].Name == "mutated" || second[0].Columns[0].Name == "mutated" || len(second[0].Indexes) == 0 {
		t.Fatalf("one caller's edit reached the next caller: %+v", second[0])
	}
}

// Open hands out PRIVATE copies: what one reader writes — rows, sequence
// positions, DDL — no other copy sees, so readers are as independent as if
// each had replayed the migrations itself. Close drops the template, and
// every database the test made is gone afterwards.
func TestTemplate_OpenHandsOutPrivateCopies(t *testing.T) {
	requireRealPG(t)
	baseURL, base := scratchServer(t)
	before := countScratch(t, base)

	tpl, err := readSet(t, writeLadder(t, templateLadder)).ApplyTemplateAt(baseURL)
	if err != nil {
		t.Fatalf("ApplyTemplateAt: %v", err)
	}
	a, err := tpl.Open()
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	ctx := context.Background()
	for _, stmt := range []string{
		`INSERT INTO billing.invoices (account_id, amount_cents) VALUES ('seed', 100)`,
		`ALTER TABLE accounts ADD COLUMN only_in_a TEXT`,
	} {
		if _, err := a.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("write to copy a (%s): %v", stmt, err)
		}
	}
	b, err := tpl.Open()
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	var invoices int
	if err := b.DB().QueryRow(`SELECT count(*) FROM billing.invoices`).Scan(&invoices); err != nil || invoices != 0 {
		t.Errorf("copy b sees %d invoice rows (err=%v): copy a's write reached it", invoices, err)
	}
	var next int64
	if err := b.DB().QueryRow(`SELECT nextval('billing.invoices_id_seq')`).Scan(&next); err != nil || next != 1 {
		t.Errorf("copy b's sequence starts at %d (err=%v), want 1: copy a's insert advanced it", next, err)
	}
	got, err := Introspect(b.DB())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, tpl.Tables()) {
		t.Error("copy b's schema differs from the template's: copy a's DDL reached it")
	}

	// Copies outlive the template: they are databases of their own.
	tpl.Close()
	tpl.Close() // idempotent
	if err := a.DB().QueryRow(`SELECT count(*) FROM billing.invoices`).Scan(&invoices); err != nil || invoices != 1 {
		t.Errorf("copy a after the template closed: rows=%d err=%v", invoices, err)
	}
	a.Close()
	b.Close()
	if after := countScratch(t, base); after != before {
		t.Errorf("scratch databases: %d before, %d after — the template or a copy was not dropped", before, after)
	}
}

// A ladder that does not apply builds no template and leaves no database
// behind.
func TestApplyTemplateAt_FailureLeavesNothing(t *testing.T) {
	requireRealPG(t)
	baseURL, base := scratchServer(t)
	before := countScratch(t, base)
	dir := t.TempDir()
	writeMig(t, dir, "00001_bad.up.sql", `CREATE TABLE broken (id TEXT PRIMARY KEY, ref TEXT REFERENCES nowhere(id));`)
	tpl, err := readSet(t, dir).ApplyTemplateAt(baseURL)
	if err == nil {
		tpl.Close()
		t.Fatal("a migration postgres rejects must fail ApplyTemplateAt")
	}
	if tpl != nil {
		t.Error("ApplyTemplateAt returned a template beside its error")
	}
	if after := countScratch(t, base); after != before {
		t.Errorf("scratch databases: %d before, %d after a failed ApplyTemplateAt", before, after)
	}
}
