package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The proto scalar vocabulary is CLOSED — protobuf defines exactly these
// fifteen kinds — so a table over it is exhaustive rather than a sample.
// Every one of them is a field an app author can write today, and entity
// birth (internal/scaffold/entityproto.go) emits a column for every one.
//
// Before this test, NINE of the fifteen projected to Go `string`
// (ProtoTypeToGoType's default arm), and the array vocabulary collapsed
// every non-int64 array column to []string. The consequences were two
// different failures wearing the same green:
//
//   - `bytes` and the unsigned/zigzag/fixed integers were REJECTED by the
//     pairing gate, so an app carrying one could not run `forge generate`
//     at all;
//   - `repeated bytes` FALSELY PASSED, because the wire side resolved to
//     []string and the column side resolved to []string, so the two wrong
//     answers agreed. The generator then emitted
//     `append([]string(nil), m.Chunks...)` against a [][]byte field, which
//     does not compile.
//
// A gate that reports a shape mapped and emits uncompilable code is worse
// than one that refuses, so the assertions below are on the EMITTED Go
// (and, in TestBornScalarShapes_ConversionsCompile, on whether it compiles)
// rather than on the pairing verdict alone.

// scalarShape is one proto scalar kind with the column entity birth emits
// for it and the Go types both halves of the conversion must resolve to.
type scalarShape struct {
	kind string // proto scalar kind, as the descriptor spells it
	// declType is the SQL type internal/scaffold's scalarSQL emits.
	declType string
	// canonical is what schemadef.MapDeclaredType makes of declType.
	canonical string
	// wireGo is the Go type protoc-gen-go generates for the field.
	wireGo string
	// dbGo is the Go type the column projects to on the entity struct.
	dbGo string
	// guarded marks a pairing whose cast cannot represent every value of
	// its source and must therefore emit a range check rather than a bare
	// conversion (see numericGuardCond).
	guardedToDB   bool
	guardedToWire bool
}

// bornScalarShapes is the whole proto scalar vocabulary, paired with the
// column birth gives it. Fifteen kinds, no sampling.
func bornScalarShapes() []scalarShape {
	i32 := func(kind string) scalarShape {
		return scalarShape{kind: kind, declType: "BIGINT", canonical: "int64",
			wireGo: "int32", dbGo: "int64", guardedToWire: true}
	}
	i64 := func(kind string) scalarShape {
		return scalarShape{kind: kind, declType: "BIGINT", canonical: "int64",
			wireGo: "int64", dbGo: "int64"}
	}
	u32 := func(kind string) scalarShape {
		return scalarShape{kind: kind, declType: "BIGINT", canonical: "int64",
			wireGo: "uint32", dbGo: "int64", guardedToWire: true}
	}
	u64 := func(kind string) scalarShape {
		return scalarShape{kind: kind, declType: "BIGINT", canonical: "int64",
			wireGo: "uint64", dbGo: "int64", guardedToDB: true, guardedToWire: true}
	}
	return []scalarShape{
		{kind: "string", declType: "TEXT", canonical: "string", wireGo: "string", dbGo: "string"},
		{kind: "bool", declType: "BOOLEAN", canonical: "bool", wireGo: "bool", dbGo: "bool"},
		{kind: "bytes", declType: "BYTEA", canonical: "bytes", wireGo: "[]byte", dbGo: "[]byte"},
		{kind: "float", declType: "DOUBLE PRECISION", canonical: "float64", wireGo: "float32", dbGo: "float64"},
		{kind: "double", declType: "DOUBLE PRECISION", canonical: "float64", wireGo: "float64", dbGo: "float64"},
		i32("int32"), i32("sint32"), i32("sfixed32"),
		i64("int64"), i64("sint64"), i64("sfixed64"),
		u32("uint32"), u32("fixed32"),
		u64("uint64"), u64("fixed64"),
	}
}

func scalarCol(name string, s scalarShape, repeated, notNull bool) EntityColumn {
	decl := s.declType
	if repeated {
		decl += "[]"
	}
	return EntityColumn{
		Name: name, Type: s.canonical, IsArray: repeated,
		DeclType: decl, NotNull: notNull,
	}
}

func scalarWire(name string, s scalarShape, repeated bool) EntityField {
	return schemaFieldToEntityField(SchemaFieldDef{Name: name, Kind: s.kind, Repeated: repeated})
}

// TestBornScalarShapes_WireGoTypeIsTheProtocGenGoType pins the projection
// itself: the Go type protoc-gen-go generates for each proto scalar. Nine
// of the fifteen used to answer "string".
func TestBornScalarShapes_WireGoTypeIsTheProtocGenGoType(t *testing.T) {
	for _, s := range bornScalarShapes() {
		if got := ProtoTypeToGoType(s.kind); got != s.wireGo {
			t.Errorf("ProtoTypeToGoType(%q) = %q, want %q", s.kind, got, s.wireGo)
		}
		// The wire field the descriptor extractor builds must carry the
		// same type, singular and repeated. `bytes` is the one kind whose
		// Go type is ALREADY a slice, so `repeated bytes` is [][]byte and
		// a singular `bytes` must NOT be classified as a repeated field.
		sing := scalarWire("f", s, false)
		if sing.GoType != s.wireGo {
			t.Errorf("%s singular: GoType = %q, want %q", s.kind, sing.GoType, s.wireGo)
		}
		if sing.Kind != FieldKindScalar {
			t.Errorf("%s singular: Kind = %q, want %q", s.kind, sing.Kind, FieldKindScalar)
		}
		rep := scalarWire("f", s, true)
		if want := "[]" + s.wireGo; rep.GoType != want {
			t.Errorf("repeated %s: GoType = %q, want %q", s.kind, rep.GoType, want)
		}
		if rep.Kind != FieldKindRepeatedScalar {
			t.Errorf("repeated %s: Kind = %q, want %q", s.kind, rep.Kind, FieldKindRepeatedScalar)
		}
	}
}

// TestBornScalarShapes_ColumnGoTypeCoversTheArrayVocabulary pins the other
// half. Every array column that is not BIGINT[] used to project to
// []string, which is what let `repeated bytes` agree with itself.
func TestBornScalarShapes_ColumnGoTypeCoversTheArrayVocabulary(t *testing.T) {
	for _, s := range bornScalarShapes() {
		if got := dbBaseGoType(scalarCol("f", s, false, true)); got != s.dbGo {
			t.Errorf("%s column (%s): dbBaseGoType = %q, want %q", s.kind, s.declType, got, s.dbGo)
		}
		want := "[]" + s.dbGo
		if got := dbBaseGoType(scalarCol("f", s, true, true)); got != want {
			t.Errorf("repeated %s column (%s[]): dbBaseGoType = %q, want %q", s.kind, s.declType, got, want)
		}
	}
}

// TestBornScalarShapes_EveryBornPairingMaps is the contract entity birth
// and the conversion generator share: birth creates a column only for what
// the generator will map onto it. Any kind that fails here is a field that
// is dead over the API — or, worse, one the gate blesses and the compiler
// then rejects.
func TestBornScalarShapes_EveryBornPairingMaps(t *testing.T) {
	for _, s := range bornScalarShapes() {
		for _, rep := range []bool{false, true} {
			for _, notNull := range []bool{true, false} {
				label := fmt.Sprintf("%s repeated=%v notnull=%v", s.kind, rep, notNull)
				// A nullable ARRAY column still projects to a bare slice,
				// so the two axes are independent and both are exercised.
				wf := scalarWire("f", s, rep)
				col := scalarCol("f", s, rep, notNull)
				to, whyTo := assignToProto("m", "e", wf, col)
				from, whyFrom := assignToDB("e", "m", wf, col)
				if whyTo != "" {
					t.Errorf("%s: no read conversion: %s", label, whyTo)
				}
				if whyFrom != "" {
					t.Errorf("%s: no write conversion: %s", label, whyFrom)
				}
				if whyTo != "" || whyFrom != "" {
					continue
				}
				// The emitted code must spell the REAL types. This is the
				// assertion the pairing verdict could not make: `repeated
				// bytes` passed the verdict while emitting []string.
				if rep {
					wrongElem := "[]string"
					if s.wireGo == "string" {
						wrongElem = ""
					}
					if wrongElem != "" && (strings.Contains(to, wrongElem) || strings.Contains(from, wrongElem)) {
						t.Errorf("%s: emitted code still spells %s — the projection collapsed the element type:\n  toProto: %s\n  toDB:    %s",
							label, wrongElem, to, from)
					}
				}
			}
		}
	}
}

// TestBornScalarShapes_LossyCastsAreGuarded pins that a cast which cannot
// represent every value of its source emits a RANGE CHECK, not a bare
// conversion.
//
// Postgres BIGINT is signed 64-bit, so it cannot hold the top half of
// uint64 — `int64(m.Size)` wraps to a negative and stores it without a
// word. The same silence runs the other way for every narrowing read: a
// BIGINT column holding 3000000000 read into an `int32` field is
// -1294967296, and nothing says so. (Postgres itself already rejects an
// out-of-range uint64 on the ARRAY path — `value "18446744073709551615" is
// out of range for type bigint` — so the Go cast is precisely what turned a
// loud database error into silent corruption.)
func TestBornScalarShapes_LossyCastsAreGuarded(t *testing.T) {
	for _, s := range bornScalarShapes() {
		wf := scalarWire("f", s, false)
		col := scalarCol("f", s, false, true)
		to, _ := assignToProto("m", "e", wf, col)
		from, _ := assignToDB("e", "m", wf, col)

		hasGuard := func(body string) bool { return strings.Contains(body, "fmt.Errorf") }
		if got := hasGuard(from); got != s.guardedToDB {
			t.Errorf("%s write: guarded = %v, want %v; emitted:\n%s", s.kind, got, s.guardedToDB, from)
		}
		if got := hasGuard(to); got != s.guardedToWire {
			t.Errorf("%s read: guarded = %v, want %v; emitted:\n%s", s.kind, got, s.guardedToWire, to)
		}
	}
}

// TestScalarPairing_FloatAndIntegerColumnsDoNotPair pins a REFUSAL.
//
// Every numeric cast used to be blessed on the sole grounds that both
// sides were numeric, including across the float/integer boundary: a
// `double` field over a
// BIGINT column emitted `int64(m.Ratio)`, dropping the fraction of every
// value ever written, and an `int64` field over a DOUBLE PRECISION column
// emitted `float64(...)`, losing exactness above 2^53. Neither round-trips
// what it was given, so neither is a conversion.
func TestScalarPairing_FloatAndIntegerColumnsDoNotPair(t *testing.T) {
	cases := []struct{ kind, declType, canonical string }{
		{"double", "BIGINT", "int64"},
		{"float", "BIGINT", "int64"},
		{"int64", "DOUBLE PRECISION", "float64"},
		{"uint32", "NUMERIC(10,2)", "float64"},
	}
	for _, c := range cases {
		wf := schemaFieldToEntityField(SchemaFieldDef{Name: "f", Kind: c.kind})
		col := EntityColumn{Name: "f", Type: c.canonical, DeclType: c.declType, NotNull: true}
		if _, why := assignToDB("e", "m", wf, col); why == "" {
			t.Errorf("%s over %s: write conversion was accepted; a float/integer pairing truncates", c.kind, c.declType)
		}
		if _, why := assignToProto("m", "e", wf, col); why == "" {
			t.Errorf("%s over %s: read conversion was accepted; a float/integer pairing truncates", c.kind, c.declType)
		}
	}
}

// TestBornScalarShapes_ConversionsCompile is the guard the pairing verdict
// could never be: it renders the generated conversion pair for every born
// scalar shape into a real Go package and COMPILES it.
//
// The wire struct is typed from protoc-gen-go's OWN mapping — the external
// truth forge does not get to define — while the entity struct is typed
// from forge's column projection. That asymmetry is the whole point: when
// `repeated bytes` collapsed to []string on both of forge's halves the two
// wrong answers agreed with each other, and every pairing assertion passed.
// The compiler does not consult forge's projection. It sees a real
// [][]byte wire field being handed to `append([]string(nil), ...)` and
// refuses.
func TestBornScalarShapes_ConversionsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a generated package with the go tool; skipped under -short")
	}

	var (
		entityFields []string
		wireFields   []string
		toProto      []string
		toDB         []string
	)
	// One field per (kind, repeated, nullable) combination, all in ONE
	// struct pair, so a single compile covers the whole vocabulary.
	for _, s := range bornScalarShapes() {
		for _, rep := range []bool{false, true} {
			for _, notNull := range []bool{true, false} {
				name := fmt.Sprintf("%s_%v_%v", s.kind, rep, notNull)
				wf := scalarWire(name, s, rep)
				col := scalarCol(name, s, rep, notNull)

				to, whyTo := assignToProto("m", "e", wf, col)
				from, whyFrom := assignToDB("e", "m", wf, col)
				if whyTo != "" || whyFrom != "" {
					continue // refusals are TestBornScalarShapes_EveryBornPairingMaps' business
				}
				// The entity struct field type must be derived the SAME way
				// the ORM generator derives it, or this compile proves
				// nothing about the code forge actually emits.
				dbType := dbBaseGoType(col)
				if dbNullable(col) {
					dbType = "*" + dbType
				}
				// protoc-gen-go's type, NOT ProtoTypeToGoType's opinion of
				// it — a projection cannot be its own oracle.
				protocType := s.wireGo
				if rep {
					protocType = "[]" + protocType
				}
				entityFields = append(entityFields, fmt.Sprintf("\t%s %s", wf.GoName, dbType))
				wireFields = append(wireFields, fmt.Sprintf("\t%s %s", wf.GoName, protocType))
				toProto = append(toProto, "\t"+to)
				toDB = append(toDB, "\t"+from)
			}
		}
	}
	if len(toProto) == 0 {
		t.Fatal("no shape produced a conversion — this test would compile an empty file and report green")
	}

	src := fmt.Sprintf(`package conv

import "fmt"

var _ = fmt.Sprintf

type Entity struct {
%s
}

type Wire struct {
%s
}

func toProto(e *Entity) (*Wire, error) {
	m := &Wire{}
%s
	return m, nil
}

func fromProto(m *Wire) (*Entity, error) {
	e := &Entity{}
%s
	return e, nil
}
`, strings.Join(entityFields, "\n"), strings.Join(wireFields, "\n"),
		strings.Join(toProto, "\n"), strings.Join(toDB, "\n"))

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module conv\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conv.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the generated conversions do not compile: %v\n%s\n--- SOURCE ---\n%s", err, out, src)
	}
}
