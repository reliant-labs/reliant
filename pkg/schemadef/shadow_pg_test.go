package schemadef

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
)

const viewLadderUp = `
CREATE TABLE chats (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    tags TEXT[] NOT NULL DEFAULT '{}'::text[]
);
CREATE VIEW chat_titles AS SELECT id, title, tags FROM chats;
COMMENT ON VIEW chat_titles IS 'forge:entity key=id';
COMMENT ON COLUMN chat_titles.title IS 'the display title';
CREATE MATERIALIZED VIEW chat_counts AS SELECT count(*) AS n FROM chats;
`

// IntrospectViews reads regular and materialized views — columns in order,
// element types for arrays, comments on the view and its columns.
func TestIntrospectViews_RegularAndMaterialized(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", viewLadderUp)
	shadow, err := OpenShadowAt(dir, "")
	if err != nil {
		t.Fatalf("OpenShadowAt: %v", err)
	}
	defer shadow.Close()

	views, err := IntrospectViews(shadow.DB())
	if err != nil {
		t.Fatalf("IntrospectViews: %v", err)
	}
	var names []string
	byName := map[string]Table{}
	for _, v := range views {
		names = append(names, v.Name)
		byName[v.Name] = v
		if !v.IsView {
			t.Errorf("%s: IsView = false", v.Name)
		}
	}
	if !slices.Equal(names, []string{"chat_counts", "chat_titles"}) {
		t.Fatalf("views = %v, want both, ordered by name", names)
	}
	titles := byName["chat_titles"]
	if ok, key := titles.DeclaresEntity(); !ok || key != "id" {
		t.Errorf("chat_titles opt-in = %v, %q", ok, key)
	}
	if len(titles.Columns) != 3 || titles.Columns[0].Name != "id" || titles.Columns[1].Name != "title" || titles.Columns[1].Comment != "the display title" {
		t.Fatalf("chat_titles columns = %+v", titles.Columns)
	}
	if tags := titles.Columns[2]; !tags.IsArray || tags.Type != TypeString || !tags.TypeKnown {
		t.Errorf("tags column = %+v, want a known string array", tags)
	}
	if n := byName["chat_counts"].Columns; len(n) != 1 || n[0].Name != "n" || n[0].Type != TypeInt {
		t.Errorf("materialized view columns = %+v", n)
	}
	// Views are not tables.
	tables, err := Introspect(shadow.DB())
	if err != nil || len(tables) != 1 || tables[0].Name != "chats" {
		t.Fatalf("Introspect = %+v, %v; want only the base table", tables, err)
	}
}

// TryApply answers "would this migration apply?" without changing the shadow,
// and attributes the first failing schema statement.
func TestShadow_TryApplyRollsBackAndReportsTheFirstSchemaError(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", `CREATE TABLE accounts (id TEXT PRIMARY KEY);`)
	shadow, err := OpenShadowAt(dir, "")
	if err != nil {
		t.Fatalf("OpenShadowAt: %v", err)
	}
	defer shadow.Close()
	ctx := context.Background()

	if err := shadow.TryApply(ctx, `ALTER TABLE accounts ADD COLUMN email TEXT; CREATE INDEX accounts_email ON accounts (email);`); err != nil {
		t.Fatalf("a valid migration: %v", err)
	}
	tables, err := Introspect(shadow.DB())
	if err != nil {
		t.Fatal(err)
	}
	if len(tables[0].Columns) != 1 {
		t.Fatalf("TryApply left %d columns, want 1: it must roll back", len(tables[0].Columns))
	}

	err = shadow.TryApply(ctx, `ALTER TABLE accounts ADD COLUMN ok TEXT; ALTER TABLE ghosts ADD COLUMN x TEXT; ALTER TABLE accounts ADD COLUMN never TEXT;`)
	if err == nil || !strings.Contains(err.Error(), "ghosts") {
		t.Fatalf("TryApply = %v, want the failure naming the missing table", err)
	}

	// A statement that cannot affect the table model may fail without
	// failing the try, and does not abort the schema statements after it.
	if err := shadow.TryApply(ctx, `CREATE EXTENSION this_extension_does_not_exist; ALTER TABLE accounts ADD COLUMN after_skip TEXT;`); err != nil {
		t.Fatalf("a skippable statement failed the try: %v", err)
	}

	// A cancelled context cannot even begin.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := shadow.TryApply(cancelled, `ALTER TABLE accounts ADD COLUMN c TEXT;`); !errors.Is(err, context.Canceled) {
		t.Fatalf("TryApply on a cancelled ctx = %v", err)
	}
}

// A project's own migration tool applies the set when it supplies a command;
// the shadow is then introspected like any other, and a failing command leaves
// no database behind.
func TestCommandMigrationSet_AppliesViaTheCommandAndIntrospects(t *testing.T) {
	requireRealPG(t)
	var gotDSN string
	set := NewCommandMigrationSet("tool-digest", 1, func(dsn string) error {
		gotDSN = dsn
		db, err := openDSN(dsn)
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.Exec(`CREATE TABLE made_by_tool (id BIGINT PRIMARY KEY, label TEXT NOT NULL)`)
		return err
	})
	tables, err := ApplyAndIntrospectSetAt(set, "")
	if err != nil {
		t.Fatalf("ApplyAndIntrospectSetAt: %v", err)
	}
	if gotDSN == "" || len(tables) != 1 || tables[0].Name != "made_by_tool" || len(tables[0].Columns) != 2 {
		t.Fatalf("dsn=%q tables=%+v", gotDSN, tables)
	}

	// The same set builds a template, with its views.
	tpl, err := set.ApplyTemplateAt("")
	if err != nil {
		t.Fatalf("ApplyTemplateAt: %v", err)
	}
	defer tpl.Close()
	if tpl.Digest() != "tool-digest" || len(tpl.Tables()) != 1 || len(tpl.Views()) != 0 {
		t.Fatalf("template: digest=%q tables=%v views=%v", tpl.Digest(), tpl.Tables(), tpl.Views())
	}

	boom := errors.New("tool exploded")
	bad := NewCommandMigrationSet("bad", 1, func(string) error { return boom })
	if _, err := ApplyAndIntrospectSetAt(bad, ""); !errors.Is(err, boom) || !strings.Contains(err.Error(), "apply migrations to shadow schema") {
		t.Fatalf("a failing command = %v", err)
	}
}

// A template carries the views of the schema it applied, deep-copied per call.
func TestTemplate_ViewsAreServedAndCopied(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", viewLadderUp)
	tpl, err := readSet(t, dir).ApplyTemplateAt("")
	if err != nil {
		t.Fatalf("ApplyTemplateAt: %v", err)
	}
	defer tpl.Close()
	first := tpl.Views()
	if len(first) != 2 {
		t.Fatalf("views = %d, want 2", len(first))
	}
	first[0].Name = "mutated"
	if got := tpl.Views()[0].Name; got == "mutated" {
		t.Fatal("one caller's edit of Views reached the next caller")
	}
}

// ApplyAndIntrospectSetShadowAt keeps the shadow open for the caller, who can
// run SQL against the applied schema.
func TestApplyAndIntrospectSetShadowAt_ReturnsALiveShadow(t *testing.T) {
	requireRealPG(t)
	dir := t.TempDir()
	writeMig(t, dir, "00001_init.up.sql", `CREATE TABLE widgets (id INT PRIMARY KEY, n INT CHECK (n > 0));`)
	tables, shadow, err := ApplyAndIntrospectShadowAt(dir, "")
	if err != nil {
		t.Fatalf("ApplyAndIntrospectShadowAt: %v", err)
	}
	defer shadow.Close()
	if len(tables) != 1 {
		t.Fatalf("tables = %+v", tables)
	}
	// postgres, not the model, is the authority on the CHECK.
	if _, err := shadow.DB().Exec(`INSERT INTO widgets VALUES (1, 0)`); err == nil {
		t.Error("the shadow accepted a row the applied CHECK forbids")
	}
	if _, err := shadow.DB().Exec(`INSERT INTO widgets VALUES (1, 5)`); err != nil {
		t.Errorf("the shadow refused a valid row: %v", err)
	}

	// A migration directory that cannot be read is an error, not an empty schema.
	unreadable := dir + "/00001_init.up.sql/not-a-dir" // a path through a file
	if _, _, err := ApplyAndIntrospectShadowAt(unreadable, ""); err == nil {
		t.Error("an unreadable directory path was accepted")
	}
	if _, err := OpenShadowAt(unreadable, ""); err == nil {
		t.Error("OpenShadowAt accepted an unreadable directory path")
	}
}

func openDSN(dsn string) (*sql.DB, error) { return sql.Open("postgres", dsn) }
