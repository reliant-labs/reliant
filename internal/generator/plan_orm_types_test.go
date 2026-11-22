package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// The ORM generator projects a column onto a struct field, and the CRUD
// conversion generator projects the SAME column onto the SAME field to
// decide whether a wire field pairs with it. Two copies of one table, and
// they disagreed: internal/codegen's dbBaseGoType collapsed every array
// column that was not BIGINT[] to []string, while this package's
// planFieldGoType knew about bytes and the unsigned integers but had no
// array vocabulary beyond []string / []int64 either.
//
// The tests below pin the projection AND pin that the two copies are one.

// TestPlanFieldGoType_ArrayVocabularyCoversEveryCanonicalType walks the
// canonical column vocabulary — the closed set schemadef.MapDeclaredType
// produces — and pins the Go type each projects to, scalar and array.
func TestPlanFieldGoType_ArrayVocabularyCoversEveryCanonicalType(t *testing.T) {
	cases := []struct{ canonical, scalar, array string }{
		{"string", "string", "[]string"},
		{"int64", "int64", "[]int64"},
		{"float64", "float64", "[]float64"},
		{"bool", "bool", "[]bool"},
		{"time", "time.Time", "[]time.Time"},
		{"bytes", "[]byte", "[][]byte"},
		// A JSONB column is json.RawMessage, the document's bytes; a JSONB[]
		// element is one document's text.
		{"json", "json.RawMessage", "[]string"},
		// The width-refined integers (codegen.GoIntType).
		{"int32", "int32", "[]int32"},
		{"int16", "int16", "[]int16"},
	}
	for _, c := range cases {
		got, ok := planFieldGoType(c.canonical)
		if !ok {
			t.Errorf("planFieldGoType(%q) reported no projection", c.canonical)
		} else if got != c.scalar {
			t.Errorf("planFieldGoType(%q) = %q, want %q", c.canonical, got, c.scalar)
		}
		arr := "[]" + c.canonical
		gotArr, okArr := planFieldGoType(arr)
		if !okArr {
			t.Errorf("planFieldGoType(%q) reported no projection", arr)
		} else if gotArr != c.array {
			t.Errorf("planFieldGoType(%q) = %q, want %q", arr, gotArr, c.array)
		}
	}
}

// TestPlanProjection_MatchesTheConversionGeneratorsProjection is the guard
// that keeps the two copies honest. A column whose ORM struct field is
// []string while the conversion generator believes it is [][]byte produces
// code that cannot compile — which is exactly what shipped.
func TestPlanProjection_MatchesTheConversionGeneratorsProjection(t *testing.T) {
	canonicals := []string{"string", "int64", "float64", "bool", "time", "bytes", "json"}
	for _, canonical := range canonicals {
		for _, isArray := range []bool{false, true} {
			col := codegen.EntityColumn{Name: "f", Type: canonical, IsArray: isArray, NotNull: true}
			// The ORM side: column → plan type → struct field type.
			plan := codegen.EntityDefToPlanEntity(codegen.EntityDef{
				Name: "E", TableName: "es", Columns: []codegen.EntityColumn{col},
			})
			if len(plan.Fields) != 1 {
				t.Fatalf("%s array=%v: plan projection dropped the column", canonical, isArray)
			}
			ormType, ormOK := planFieldGoType(plan.Fields[0].Type)
			if !ormOK {
				t.Fatalf("%s array=%v: the ORM side has no projection for a type the "+
					"column projection produces", canonical, isArray)
			}
			// The conversion side, reached through the exported projection
			// both now share.
			convType, convOK := codegen.CanonicalGoTypeOK(canonical, isArray)
			if !convOK {
				t.Fatalf("%s array=%v: the conversion side has no projection for a "+
					"canonical type", canonical, isArray)
			}
			if ormType != convType {
				t.Errorf("%s array=%v: ORM struct field is %q but the conversion generator pairs against %q — "+
					"generated code that assigns one to the other cannot compile",
					canonical, isArray, ormType, convType)
			}
		}
	}
}

// TestPlanFieldGoType_CoversEveryTypeTheProducerCanEmit derives its
// obligation from the PRODUCER of PlanEntityField.Type rather than from a
// list written here.
//
// codegen.planTypeForColumn fills that field by passing a column's
// canonical type through verbatim, "[]"-prefixed for an array, so the set
// of plan types a real projection can produce is exactly
// schemadef.CanonicalTypes() crossed with array-ness. Building the plan
// field by running a column through the real projection — rather than
// spelling the type here — means a canonical type added upstream without a
// Go projection fails here instead of reaching the entity struct as text.
func TestPlanFieldGoType_CoversEveryTypeTheProducerCanEmit(t *testing.T) {
	canonicals := schemadef.CanonicalTypes()
	if len(canonicals) == 0 {
		t.Fatal("schemadef.CanonicalTypes() is EMPTY — the obligation below is " +
			"derived from it, so an empty set would loop zero times and pass " +
			"while proving nothing")
	}
	if len(canonicals) < 7 {
		t.Fatalf("schemadef.CanonicalTypes() has %d members, expected at least 7: %v — "+
			"the vocabulary shrank, which silently narrows this check", len(canonicals), canonicals)
	}

	for _, ct := range canonicals {
		for _, isArray := range []bool{false, true} {
			plan := codegen.EntityDefToPlanEntity(codegen.EntityDef{
				Name: "E", TableName: "es",
				Columns: []codegen.EntityColumn{
					{Name: "f", Type: string(ct), IsArray: isArray, NotNull: true},
				},
			})
			if len(plan.Fields) != 1 {
				t.Fatalf("%s array=%v: the column projection dropped the column", ct, isArray)
			}
			planType := plan.Fields[0].Type
			if _, ok := planFieldGoType(planType); !ok {
				t.Errorf("planFieldGoType(%q) has no projection, but %q array=%v is a "+
					"type a real column produces — GeneratePlanORM would refuse to "+
					"generate an entity carrying this column", planType, ct, isArray)
			}
		}
	}
}

// TestGeneratePlanORM_RefusesOutOfVocabularyFieldType pins the refusal.
//
// The defect is not hypothetical: hand-written fixtures in this package
// once carried "int32" before it was a plan type, and while planFieldGoType
// routed through a total-signature projection it came back "string" — an
// integer field declared on the entity struct as text, generated green.
// ("int32"/"int16" are plan types now, the width-refined integers.) The
// generator must name the entity, the field and the type, and write nothing.
func TestGeneratePlanORM_RefusesOutOfVocabularyFieldType(t *testing.T) {
	for _, bad := range []string{"uint64", "float", "double", "timestamp", "[]uint64", ""} {
		t.Run("type="+bad, func(t *testing.T) {
			root := t.TempDir()
			err := GeneratePlanORM(root, config.ORMConfig{}, []config.PlanEntity{{
				Name: "Counter",
				Fields: []config.PlanEntityField{
					{Name: "counter_id", Type: "int64", PrimaryKey: true},
					{Name: "value", Type: bad, NotNull: true},
				},
			}}, nil)
			if err == nil {
				t.Fatalf("GeneratePlanORM accepted field type %q — it would be declared "+
					"on the entity struct as text", bad)
			}
			for _, want := range []string{"Counter", "value", bad} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %q: %v", want, err)
				}
			}
			// Nothing may be written for a rejected entity.
			if _, statErr := os.Stat(filepath.Join(root, "internal", "db")); statErr == nil {
				t.Error("internal/db was created for an entity that was refused")
			}
		})
	}
}

// TestRenderORMEntity_EmitsNativeArrayFieldTypes pins the actual emitted
// struct: a BOOLEAN[] column is a []bool field with `,array`, not a
// []string one. Asserted on the render rather than the helper because the
// struct is what Bun binds and scans.
func TestRenderORMEntity_EmitsNativeArrayFieldTypes(t *testing.T) {
	ent := config.PlanEntity{
		Name: "Sample", TableName: "samples",
		Fields: []config.PlanEntityField{
			{Name: "id", Type: "string", PrimaryKey: true, NotNull: true},
			{Name: "flags", Type: "[]bool", NotNull: true},
			{Name: "scores", Type: "[]float64", NotNull: true},
			{Name: "chunks", Type: "[]bytes", NotNull: true},
			{Name: "marks", Type: "[]time", NotNull: true},
			{Name: "tags", Type: "[]string", NotNull: true},
			{Name: "ids", Type: "[]int64", NotNull: true},
		},
	}
	src := string(renderORMEntity("db", ent, false))
	for _, want := range []string{
		"Flags []bool `bun:\"flags,array,notnull\"`",
		"Scores []float64 `bun:\"scores,array,notnull\"`",
		"Chunks [][]byte `bun:\"chunks,array,notnull\"`",
		"Marks []time.Time `bun:\"marks,array,notnull\"`",
		"Tags []string `bun:\"tags,array,notnull\"`",
		"Ids []int64 `bun:\"ids,array,notnull\"`",
	} {
		// gofmt aligns the struct fields, so compare on collapsed spacing.
		if !strings.Contains(collapseSpace(src), collapseSpace(want)) {
			t.Errorf("generated ORM struct is missing %q\n--- SOURCE ---\n%s", want, src)
		}
	}
	// A []time.Time field means the file must import time.
	if !strings.Contains(src, `"time"`) {
		t.Error("a []time.Time column must pull in the time import")
	}
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }
