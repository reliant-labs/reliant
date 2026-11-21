package seedplan

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// ─────────────────────────────────────────────────────────────────────────────
// The seeder reads a table's SHAPE, never its NAME.
//
// A generator that infers domain meaning from an identifier is wrong in a way
// that looks right: the guess succeeds on the vocabulary it was written for and
// fails silently everywhere else. These tests pin the invariant on both halves
// of the seeder — the referential half (which value a key carries) and the
// vocabulary half (which pool a string column draws from).
// ─────────────────────────────────────────────────────────────────────────────

// tableNameSweep is the set of names the same table shape is built under. It is
// an INPUT, not an expectation: every assertion below derives what it wants
// from the emitter's own vocabulary and key functions, and only requires that
// the answer not move as the name does. The names are chosen to span the
// conventional identity/organization/person spellings a heuristic would key on,
// plus neutral ones that no English word list contains.
var tableNameSweep = []string{
	"users", "user", "accounts", "staff", "members", "principals",
	"organizations", "orgs", "companies", "brands", "vendors", "teams",
	"clinics", "widgets", "zz_records",
}

// keySchema returns one table of the given name whose primary key is `id`,
// declared with declType, plus a plain payload column.
func keySchema(name, declType string) []schemadef.Table {
	idCol := col("id", schemadef.TypeString, true, true)
	idCol.DeclType = declType
	return []schemadef.Table{{
		Name:    name,
		PKCols:  []string{"id"},
		Columns: []schemadef.Column{idCol, col("label", schemadef.TypeString, true, false)},
	}}
}

// Every primary key the seeder writes comes from its key function: an integer
// PK is a bare index, and every other PK is a canonical UUIDv5 (see pkLiteral /
// deterministicUUID). That shape is the emitter's own stamp — it is what makes
// a foreign key resolve by construction, and it is what a `uuid`-declared
// column accepts on INSERT. No table name may change it.
func TestPrimaryKeysAlwaysCarryTheKeyFunctionsShape(t *testing.T) {
	checked := 0
	for _, declType := range []string{"TEXT", "UUID"} {
		for _, name := range tableNameSweep {
			p := buildOrFail(t, keySchema(name, declType), Config{Rows: 4, Salt: 3})
			for i, lit := range cellsFor(p, name, "id") {
				raw, ok := decodeScalarLiteral(lit)
				if !ok {
					t.Fatalf("%s(%s).id row %d = %s is not a scalar literal", name, declType, i, lit)
				}
				if !uuidLiteralRE.MatchString(raw) {
					t.Errorf("%s(%s).id row %d = %q is not the UUID the key function produces — "+
						"the seeder decided something about this table from its NAME",
						name, declType, i, raw)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no primary-key cells were checked — the sweep derived an empty set, so this test proves nothing")
	}
}

// nameShape is one column layout the same `name` column is declared inside.
type nameShape struct {
	what    string
	columns []string
}

// What a `name` column carries is the emitter's placeholder, and no table name
// and no sibling column moves it. The value is checked against the stamp the
// emitter itself puts on everything it invents (SyntheticStringPrefix), so
// this cannot rot into a copied list of literals.
func TestNameSynthesisIsInvariantUnderTheTableName(t *testing.T) {
	if SyntheticStringPrefix == "" {
		t.Fatal("the emitter stamps nothing — every assertion below would hold vacuously")
	}
	shapes := []nameShape{
		{"name alone", []string{"name"}},
		{"name + sku", []string{"name", "sku"}},
		{"name + email", []string{"name", "email"}},
		{"name + first_name", []string{"name", "first_name"}},
		{"name + date_of_birth", []string{"name", "date_of_birth"}},
	}

	checked := 0
	for _, sh := range shapes {
		for _, name := range tableNameSweep {
			tb := schemadef.Table{Name: name, PKCols: []string{"id"},
				Columns: []schemadef.Column{col("id", schemadef.TypeString, true, true)}}
			for _, c := range sh.columns {
				tb.Columns = append(tb.Columns, col(c, schemadef.TypeString, true, false))
			}
			p := buildOrFail(t, []schemadef.Table{tb}, Config{Rows: 5, Salt: 7})
			for i, lit := range cellsFor(p, name, "name") {
				raw, ok := decodeScalarLiteral(lit)
				if !ok {
					t.Fatalf("%s.name row %d = %s is not a scalar literal", name, i, lit)
				}
				if !strings.HasPrefix(raw, SyntheticStringPrefix) {
					t.Errorf("table %q, shape %q, row %d: name = %q is not the emitter's "+
						"placeholder — the table's NAME or a sibling column decided what a "+
						"`name` column holds", name, sh.what, i, raw)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no name cells were checked — the sweep derived an empty set, so this test proves nothing")
	}
}
