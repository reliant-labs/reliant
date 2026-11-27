package schemadef

import (
	"slices"
	"strings"
	"testing"
)

// The table-level declarations live in the catalog comment; each accessor
// reads exactly its own marker, as a whole token.
func TestTableDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		table      Table
		entityName string
		declares   bool
		key        string
		noEntity   bool
		appendOnly bool
		declared   bool // AppendOnlyDeclared: the comment, not the trigger
	}{
		{name: "no declaration", table: Table{Name: "t", Comment: "just prose"}},
		{name: "entity name", table: Table{Name: "daemon_attachment", Comment: "forge:entity-name=DaemonAttachment"}, entityName: "DaemonAttachment"},
		{name: "entity name among prose", table: Table{Comment: "legacy table; forge:entity-name=Widget_2 trailing"}, entityName: "Widget_2"},
		{name: "entity name needs an identifier", table: Table{Comment: "forge:entity-name=9bad"}},
		{name: "entity opt-in", table: Table{Comment: "forge:entity"}, declares: true},
		{name: "entity opt-in with a key", table: Table{Comment: "forge:entity key=chat_id"}, declares: true, key: "chat_id"},
		{name: "entity opt-in inside prose", table: Table{Comment: "a view; forge:entity. ok"}, declares: true},
		{name: "entity-name is not the entity opt-in", table: Table{Comment: "forge:entity-name=Foo"}, entityName: "Foo"},
		{name: "no-entity", table: Table{Comment: "forge:no-entity"}, noEntity: true},
		{name: "no-entity is a whole token", table: Table{Comment: "forge:no-entity-ever"}},
		{name: "append-only comment", table: Table{Name: "ledger", Comment: "forge:append-only"}, appendOnly: true, declared: true},
		{name: "append-only is a whole token", table: Table{Name: "ledger", Comment: "forge:append-only-soft"}},
		{name: "append-only by forge's guard trigger", table: Table{Name: "ledger", Triggers: []string{"ledger_append_only"}}, appendOnly: true},
		{name: "an updated_at trigger is not a guard", table: Table{Name: "ledger", Triggers: []string{"ledger_touch"}}},
		{name: "another table's guard is not this table's", table: Table{Name: "ledger", Triggers: []string{"other_append_only"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.table.DeclaredEntityName(); got != tc.entityName {
				t.Errorf("DeclaredEntityName = %q, want %q", got, tc.entityName)
			}
			ok, key := tc.table.DeclaresEntity()
			if ok != tc.declares || key != tc.key {
				t.Errorf("DeclaresEntity = %v, %q; want %v, %q", ok, key, tc.declares, tc.key)
			}
			if got := tc.table.NoEntity(); got != tc.noEntity {
				t.Errorf("NoEntity = %v, want %v", got, tc.noEntity)
			}
			if got := tc.table.AppendOnly(); got != tc.appendOnly {
				t.Errorf("AppendOnly = %v, want %v", got, tc.appendOnly)
			}
			if got := tc.table.AppendOnlyDeclared(); got != tc.declared {
				t.Errorf("AppendOnlyDeclared = %v, want %v", got, tc.declared)
			}
		})
	}
}

func TestAppendOnlyGuardTrigger_IsTheNameAppendOnlyRecognizes(t *testing.T) {
	name := AppendOnlyGuardTrigger("ledger")
	if name != "ledger_append_only" {
		t.Fatalf("AppendOnlyGuardTrigger = %q", name)
	}
	if !(Table{Name: "ledger", Triggers: []string{name}}).AppendOnly() {
		t.Fatal("a table carrying the guard trigger forge names is not append-only")
	}
}

func TestColumnGoTypeOverride(t *testing.T) {
	for _, tc := range []struct{ comment, want string }{
		{"", ""},
		{"prose only", ""},
		{"forge:go-type=map[string]any", "map[string]any"},
		{"forge:go-type=github.com/acme/app/gen/app/v1;appv1.ContentBlockType trailing", "github.com/acme/app/gen/app/v1;appv1.ContentBlockType"},
	} {
		if got := (Column{Comment: tc.comment}).GoTypeOverride(); got != tc.want {
			t.Errorf("GoTypeOverride(%q) = %q, want %q", tc.comment, got, tc.want)
		}
	}
}

func TestIndexKeyList(t *testing.T) {
	// A hand-built index with only Columns derives one plain key per column.
	ix := Index{Columns: []string{"a", "b"}}
	want := []IndexKey{{Expr: "a", Column: "a"}, {Expr: "b", Column: "b"}}
	if got := ix.KeyList(); !slices.Equal(got, want) {
		t.Errorf("KeyList from Columns = %+v, want %+v", got, want)
	}
	if got := (Index{}).KeyList(); len(got) != 0 {
		t.Errorf("KeyList of an empty index = %+v", got)
	}
	// An introspected index's own Keys win.
	keys := []IndexKey{{Expr: "lower(email)", Column: "email"}}
	if got := (Index{Columns: []string{"email"}, Keys: keys}).KeyList(); !slices.Equal(got, keys) {
		t.Errorf("KeyList with Keys = %+v, want %+v", got, keys)
	}
}

func TestParseFoldedKey(t *testing.T) {
	for _, tc := range []struct{ in, col, fold string }{
		{"lower(email)", "email", "lower"},
		{"UPPER(name)", "name", "upper"},
		{`lower("Email")`, "Email", "lower"},
		{"lower((email)::text)", "email", "lower"},
		{"  lower( email )  ", "email", "lower"},
		{"lower(a || b)", "", ""},
		{"abs(x)", "", ""},
		{"email", "", ""},
		{"", "", ""},
	} {
		col, fold := parseFoldedKey(tc.in)
		if col != tc.col || fold != tc.fold {
			t.Errorf("parseFoldedKey(%q) = %q, %q; want %q, %q", tc.in, col, fold, tc.col, tc.fold)
		}
	}
}

// CanonicalTypes is derived from the declared SQL type map, so it names every
// type a column can really carry — sorted, once each.
func TestCanonicalTypes(t *testing.T) {
	got := CanonicalTypes()
	if !slices.IsSorted(got) {
		t.Errorf("CanonicalTypes is not sorted: %v", got)
	}
	if len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Errorf("CanonicalTypes repeats a type: %v", got)
	}
	for _, want := range []CanonicalType{TypeString, TypeInt, TypeFloat, TypeBool, TypeTime, TypeJSON, TypeBytes} {
		if !slices.Contains(got, want) {
			t.Errorf("CanonicalTypes misses %q: %v", want, got)
		}
	}
	// Every type a declared spelling maps to is in the list.
	for _, decl := range []string{"TEXT", "BIGINT", "BOOLEAN", "TIMESTAMPTZ", "JSONB", "BYTEA", "DOUBLE PRECISION"} {
		c, _, known := MapDeclaredType(decl)
		if !known || !slices.Contains(got, c) {
			t.Errorf("MapDeclaredType(%q) = %q (known %v), not in CanonicalTypes %v", decl, c, known, got)
		}
	}
}

func TestStripLeadingSQLComments(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"  \n\tSELECT 1", "SELECT 1"},
		{"-- banner\nSELECT 1", "SELECT 1"},
		{"-- one\n-- two\n\nSELECT 1", "SELECT 1"},
		{"/* block */ SELECT 1", "SELECT 1"},
		{"/* a */ -- b\n/* c */SELECT 1", "SELECT 1"},
		{"-- only a comment", ""},
		{"/* never closed", ""},
		{"", ""},
		{"SELECT 1 -- trailing stays", "SELECT 1 -- trailing stays"},
	} {
		if got := stripLeadingSQLComments(tc.in); got != tc.want {
			t.Errorf("stripLeadingSQLComments(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsSchemaDefining(t *testing.T) {
	for stmt, want := range map[string]bool{
		"CREATE TABLE t (id int)":                   true,
		"  create   table t (id int)":               true,
		"-- hdr\nCREATE UNIQUE INDEX i ON t (a)":    true,
		"/* x */ ALTER TABLE t ADD COLUMN a int":    true,
		"DROP INDEX i":                              true,
		"COMMENT ON TABLE t IS 'x'":                 false,
		"CREATE FUNCTION f() RETURNS int AS $$$$ ;": false,
		"INSERT INTO t VALUES (1)":                  false,
		"-- only a comment":                         false,
	} {
		if got := isSchemaDefining(stmt); got != want {
			t.Errorf("isSchemaDefining(%q) = %v, want %v", stmt, got, want)
		}
	}
}

func TestMigrationSet_CommandSetsAndEmptyTemplates(t *testing.T) {
	applied := ""
	set := NewCommandMigrationSet("digest-1", 3, func(dsn string) error { applied = dsn; return nil })
	if set.Len() != 3 || set.Digest() != "digest-1" {
		t.Fatalf("command set: Len=%d Digest=%q", set.Len(), set.Digest())
	}
	if applied != "" {
		t.Fatal("building a set ran its command")
	}

	// A set with nothing in it has nothing to apply, and says so rather than
	// booting a postgres for an empty template.
	empty := NewCommandMigrationSet("e", 0, nil)
	if _, err := empty.ApplyTemplateAt(""); err == nil || !strings.Contains(err.Error(), "no migrations to apply to a template") {
		t.Fatalf("ApplyTemplateAt on an empty set = %v", err)
	}
	if tables, shadow, err := ApplyAndIntrospectSetShadowAt(nil, ""); tables != nil || shadow != nil || err != nil {
		t.Fatalf("a nil set = %v, %v, %v; want nothing", tables, shadow, err)
	}
	if tables, err := ApplyAndIntrospectSetAt(empty, ""); tables != nil || err != nil {
		t.Fatalf("an empty set = %v, %v; want nothing", tables, err)
	}
}

func TestShadow_CloseIsNilSafeAndIdempotent(t *testing.T) {
	var nilShadow *Shadow
	nilShadow.Close()

	calls := 0
	s := &Shadow{cleanup: func() { calls++ }}
	s.Close()
	s.Close()
	if calls != 1 {
		t.Fatalf("cleanup ran %d times, want once", calls)
	}

	var nilTemplate *Template
	nilTemplate.Close()
	tpl := &Template{cleanup: func() { calls++ }}
	tpl.Close()
	tpl.Close()
	if calls != 2 {
		t.Fatalf("template cleanup ran %d extra times, want once", calls-1)
	}
}
