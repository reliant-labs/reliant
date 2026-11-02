package codegen

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

func TestColumnInsertDefault(t *testing.T) {
	col := func(canonical, def string) InsertDefaultColumn {
		return InsertDefaultColumn{Canonical: canonical, NotNull: true, Default: def}
	}
	cases := []struct {
		name string
		c    InsertDefaultColumn
		want InsertDefault
	}{
		// reliant's measured cases: the DEFAULT is not the Go zero.
		{"int DEFAULT 2", col("int64", "2"), InsertDefaultPointer},
		{"bool DEFAULT true", col("bool", "true"), InsertDefaultPointer},
		{"text DEFAULT 'draft'", col("string", "'draft'::text"), InsertDefaultPointer},
		{"varchar DEFAULT 'llm'", col("string", "'llm'::character varying"), InsertDefaultPointer},
		{"negative literal", col("int64", "(-1)"), InsertDefaultPointer},
		{"expression", col("string", "(gen_random_uuid())::text"), InsertDefaultPointer},
		{"jsonb DEFAULT '{}'", col("json", "'{}'::jsonb"), InsertDefaultNullZero},
		{"jsonb DEFAULT '[]'", col("json", "'[]'::jsonb"), InsertDefaultNullZero},
		// A born enum column: its CHECK refuses "", so "" already means
		// unset and the field keeps its type.
		{"enum text, CHECK rejects ''", InsertDefaultColumn{Canonical: "string", NotNull: true, Default: "'ORDER_STATUS_OPEN'::text", ZeroRejected: true}, InsertDefaultNullZero},
		{"int, CHECK rejects 0", InsertDefaultColumn{Canonical: "int64", NotNull: true, Default: "1", ZeroRejected: true}, InsertDefaultNullZero},
		{"text[] DEFAULT '{inbox}'", InsertDefaultColumn{Canonical: "string", IsArray: true, NotNull: true, Default: "'{inbox}'::text[]"}, InsertDefaultNullZero},

		// The DEFAULT IS the zero the struct carries: nothing to say.
		{"int DEFAULT 0", col("int64", "0"), InsertDefaultNone},
		{"bigint DEFAULT (0)::bigint", col("int64", "(0)::bigint"), InsertDefaultNone},
		{"float DEFAULT 0.0", col("float64", "0.0"), InsertDefaultNone},
		{"numeric DEFAULT '0'", col("float64", "'0'::numeric"), InsertDefaultNone},
		{"bool DEFAULT false", col("bool", "false"), InsertDefaultNone},
		{"text DEFAULT ''", col("string", "''::text"), InsertDefaultNone},
		{"text[] DEFAULT '{}'", InsertDefaultColumn{Canonical: "string", IsArray: true, NotNull: true, Default: "'{}'::text[]"}, InsertDefaultNone},
		{"text[] DEFAULT ARRAY[]", InsertDefaultColumn{Canonical: "string", IsArray: true, NotNull: true, Default: "ARRAY[]::text[]"}, InsertDefaultNone},

		// Columns with a lifecycle of their own, or nothing to default.
		{"nullable", InsertDefaultColumn{Canonical: "int64", Default: "2"}, InsertDefaultNone},
		{"no default", col("int64", ""), InsertDefaultNone},
		{"pk", InsertDefaultColumn{Canonical: "string", NotNull: true, IsPK: true, Default: "gen_random_uuid()"}, InsertDefaultNone},
		{"generated", InsertDefaultColumn{Canonical: "int64", NotNull: true, Generated: true, Default: "1"}, InsertDefaultNone},
		{"version", InsertDefaultColumn{Canonical: "int64", NotNull: true, Version: true, Default: "1"}, InsertDefaultNone},
		{"timestamp", col("time", "now()"), InsertDefaultNone},
	}
	for _, c := range cases {
		if got := ColumnInsertDefault(c.c); got != c.want {
			t.Errorf("%s: ColumnInsertDefault = %d, want %d", c.name, got, c.want)
		}
	}
}

// The CRUD conversion agrees with the struct: a pointer column is written
// through a pointer, and a create request that does not carry it leaves it
// nil — the INSERT omits it and the DEFAULT fires, with no literal pasted.
func TestCreateOp_LeavesADefaultedColumnUnset(t *testing.T) {
	status := EntityColumn{Name: "status", Type: "int64", NotNull: true, DeclType: "INTEGER", Default: "2"}
	if !dbNullable(status) {
		t.Fatal("a NOT NULL column whose DEFAULT is not its zero must be a pointer field")
	}
	svc, m, entity := readOnlyEntity(status)
	assigns, unmapped := buildCreateAssigns(svc, m, entity)
	if len(unmapped) != 0 || len(assigns) != 0 {
		t.Errorf("create op = %v (unmapped %v), want no assignment: the field stays nil", assigns, unmapped)
	}
}

func TestCheckRejectsZero(t *testing.T) {
	str := schemadef.Column{Name: "status", Type: schemadef.TypeString}
	num := schemadef.Column{Name: "n", Type: schemadef.TypeInt}
	one, neg := int64(1), int64(-1)
	cases := []struct {
		name  string
		c     schemadef.Column
		pool  []string
		bound seedplan.NumBound
		want  bool
	}{
		{"enum vocabulary without ''", str, []string{"OPEN", "CLOSED"}, seedplan.NumBound{}, true},
		{"vocabulary admitting ''", str, []string{"", "OPEN"}, seedplan.NumBound{}, false},
		{"no check", str, nil, seedplan.NumBound{}, false},
		{"integer vocabulary without 0", num, []string{"1", "2"}, seedplan.NumBound{}, true},
		{"integer vocabulary with 0", num, []string{"0", "1"}, seedplan.NumBound{}, false},
		{"bound > 0", num, nil, seedplan.NumBound{Min: &one}, true},
		{"bound < 0", num, nil, seedplan.NumBound{Max: &neg}, true},
		{"bound admitting 0", num, nil, seedplan.NumBound{Max: &one}, false},
	}
	for _, c := range cases {
		if got := checkRejectsZero(c.c, c.pool, c.bound); got != c.want {
			t.Errorf("%s: checkRejectsZero = %v, want %v", c.name, got, c.want)
		}
	}
}

// A born enum column keeps its string type (its CHECK refuses ""), and an
// unset enum leaves the field "" so the INSERT omits it and the DEFAULT fires.
func TestEnumWrite_BornEnumColumnStaysAString(t *testing.T) {
	col := EntityColumn{Name: "status", Type: "string", NotNull: true, DeclType: "TEXT",
		Default: "'ORDER_STATUS_OPEN'::text", ZeroRejected: true}
	if dbNullable(col) {
		t.Fatal("a born enum column must not become a pointer: its CHECK already makes \"\" mean unset")
	}
	svc, entity := enumUnsetEntity(col)
	conv, unmapped := BuildEntityConv(svc, entity)
	if len(unmapped) != 0 {
		t.Fatalf("unmapped: %+v", unmapped)
	}
	from := strings.Join(conv.FromProtoAssigns, "\n")
	if !strings.Contains(from, "if m.Status != 0 {\n\t\te.Status = m.Status.String()\n\t}") {
		t.Errorf("an unset enum must leave the ,nullzero field empty; got:\n%s", from)
	}
	if strings.Contains(from, "ORDER_STATUS_OPEN") {
		t.Errorf("the DEFAULT is the database's to apply:\n%s", from)
	}
}
