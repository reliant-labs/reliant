package codegen

import (
	"sort"
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Every test in this file derives its obligation from a table the
// PRODUCER actually computes — ProtoScalarKinds() for the proto scalar
// vocabulary, schemadef.CanonicalTypes() for the SQL one — rather than
// restating the vocabulary here. A test carrying its own list checks the
// kinds someone remembered, which is how `bytes` reached a generated
// frontend as 33 tsc errors with every test green.
//
// Each derived set is asserted non-empty before it is used. A loop over an
// empty set passes without executing its body, so an obligation derived
// from a producer that returns nothing is a guard that cannot fail — the
// exact defect class these tests exist to close.

// requireKinds fails when the derived vocabulary is empty or has lost
// members, so a table that silently stops producing cannot turn every
// obligation below into a vacuous pass.
func requireKinds(t *testing.T, name string, kinds []string, atLeast int) {
	t.Helper()
	if len(kinds) == 0 {
		t.Fatalf("%s is EMPTY — every obligation derived from it is vacuous, "+
			"so these tests would pass while proving nothing", name)
	}
	if len(kinds) < atLeast {
		t.Fatalf("%s has %d members, expected at least %d — the vocabulary shrank, "+
			"which silently narrows every check derived from it", name, len(kinds), atLeast)
	}
}

// TestProtoScalarGo_KeySetIsTheClosedVocabulary pins the Go projection's
// key set EQUAL to the TypeScript projection's, in both directions.
//
// The two halves of one projection drifting apart is not hypothetical: it
// is precisely how `repeated bytes` came to resolve to []string on the Go
// side and string[] on the TS side, where the two wrong answers agreed
// with each other and looked exactly like a mapped pairing. Neither table
// can now gain or lose a kind without this failing by name.
func TestProtoScalarGo_KeySetIsTheClosedVocabulary(t *testing.T) {
	tsKinds := ProtoScalarKinds()
	requireKinds(t, "ProtoScalarKinds()", tsKinds, 15)

	goKinds := make([]string, 0, len(protoScalarGo))
	for k := range protoScalarGo {
		goKinds = append(goKinds, k)
	}
	sort.Strings(goKinds)

	if len(goKinds) != len(tsKinds) {
		t.Fatalf("protoScalarGo has %d kinds, protoScalarTS has %d: %v vs %v",
			len(goKinds), len(tsKinds), goKinds, tsKinds)
	}
	for i, k := range tsKinds {
		if goKinds[i] != k {
			t.Errorf("vocabulary mismatch at %d: protoScalarGo has %q, protoScalarTS has %q",
				i, goKinds[i], k)
		}
	}
}

// TestProtoScalarProjections_AreTotalOverTheVocabulary walks the derived
// vocabulary and requires every consumer of it to answer for every kind.
//
// These are the sites that used to carry their own copy of the fifteen
// names — the raw scanner's set, the CRUD conversion's case list, the KCL
// config switch. A kind missing from any of
// them did not error; it took that copy's permissive answer (a message
// type name, `string`, `str`), which is indistinguishable from a
// correct answer for the kinds those really are.
func TestProtoScalarProjections_AreTotalOverTheVocabulary(t *testing.T) {
	kinds := ProtoScalarKinds()
	requireKinds(t, "ProtoScalarKinds()", kinds, 15)

	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			if !IsProtoScalarKind(kind) {
				t.Errorf("IsProtoScalarKind(%q) = false — the raw .proto scanner "+
					"would classify this kind as a message TYPE NAME, not a scalar", kind)
			}
			if !isScalarProtoKind(kind) {
				t.Errorf("isScalarProtoKind(%q) = false — a jsonb map with this "+
					"value kind would silently get no encoder pairing", kind)
			}
			goType, ok := ProtoScalarGoType(kind)
			if !ok || goType == "" {
				t.Fatalf("ProtoScalarGoType(%q) = (%q, %v), want a Go type", kind, goType, ok)
			}
			if got := ProtoTypeToGoType(kind); got != goType {
				t.Errorf("ProtoTypeToGoType(%q) = %q, table says %q", kind, got, goType)
			}
			// The KCL config projection: total, and never the `str`
			// fallback for a kind that is not textual.
			kcl := kclTypeForProtoConfig(ConfigField{ProtoType: kind})
			if kcl == "" {
				t.Fatalf("kclTypeForProtoConfig(%q) = \"\"", kind)
			}
			if wantInt := goType == "int32" || goType == "int64" ||
				goType == "uint32" || goType == "uint64"; wantInt && kcl != "int" {
				t.Errorf("kclTypeForProtoConfig(%q) = %q, want \"int\" (Go type %q) — "+
					"an integer config field typed as a string in the KCL schema "+
					"is validated as text against the operator's values", kind, kcl, goType)
			}
		})
	}
}

// TestProtoScalarProjections_RefuseNonScalars pins the other half: a kind
// OUTSIDE the vocabulary must be refused by the ok-returning projections
// rather than answered.
//
// This is the half that cannot be checked by walking the table, and the
// half that matters most — the defect was never a wrong answer for a real
// kind, it was a confident answer for a kind that was never in the set.
func TestProtoScalarProjections_RefuseNonScalars(t *testing.T) {
	// "message", "enum" and "map" are real proto kinds that are not
	// scalars; the last is a kind no protobuf will ever define.
	for _, kind := range []string{"message", "enum", "map", "group", "", "int128"} {
		t.Run("kind="+kind, func(t *testing.T) {
			if IsProtoScalarKind(kind) {
				t.Errorf("IsProtoScalarKind(%q) = true", kind)
			}
			if _, ok := ProtoScalarGoType(kind); ok {
				t.Errorf("ProtoScalarGoType(%q) claimed a Go type", kind)
			}
			if _, ok := protoScalarTSType(kind); ok {
				t.Errorf("protoScalarTSType(%q) claimed a TypeScript type", kind)
			}
		})
	}
}

// TestCanonicalGoType_IsTotalOverTheCanonicalVocabulary walks the SQL-side
// vocabulary — derived from schemadef's declared-type table, so it is the
// set of canonical types a real column can actually produce — and requires
// a Go projection for each.
//
// This vocabulary is DELIBERATELY separate from the proto scalar one
// above. The names overlap ("string", "bool", "bytes") but the sets do
// not: a column is never `sfixed32`, and a proto field is never `time` or
// `json`. Routing one through the other would map kinds that have no
// business mapping.
func TestCanonicalGoType_IsTotalOverTheCanonicalVocabulary(t *testing.T) {
	types := schemadef.CanonicalTypes()
	if len(types) == 0 {
		t.Fatal("schemadef.CanonicalTypes() is EMPTY — the canonical vocabulary is " +
			"derived from the declared-SQL-type table's value set, so an empty " +
			"result means no SQL type maps to anything and this check is vacuous")
	}
	if len(types) < 7 {
		t.Fatalf("schemadef.CanonicalTypes() has %d members, expected at least 7: %v",
			len(types), types)
	}

	for _, ct := range types {
		for _, isArray := range []bool{false, true} {
			goType, ok := CanonicalGoTypeOK(string(ct), isArray)
			if !ok {
				t.Errorf("CanonicalGoTypeOK(%q, array=%v) has no mapping — a column of "+
					"this type would be declared on the entity struct as text",
					ct, isArray)
				continue
			}
			if goType == "" {
				t.Errorf("CanonicalGoTypeOK(%q, array=%v) = \"\"", ct, isArray)
			}
		}
	}
}

// TestCanonicalGoType_RefusesNonCanonicalTypes pins that the projection
// reports rather than answers for a type outside the vocabulary.
//
// "int32" and "int16" are NOT in this list: they are the width-refined
// integer plan types (GoIntType) an `integer` / `smallint` column carries.
// "uint64" is the shape of the defect this guards — the spelling that once
// reached this projection and was declared as a Go string in silence.
func TestCanonicalGoType_RefusesNonCanonicalTypes(t *testing.T) {
	for _, notCanonical := range []string{"uint64", "float", "double", "money", ""} {
		if _, ok := CanonicalGoTypeOK(notCanonical, false); ok {
			t.Errorf("CanonicalGoTypeOK(%q) claimed a mapping; %q is not a canonical "+
				"schema type", notCanonical, notCanonical)
		}
	}
}

// TestCanonicalTypes_MatchesMapDeclaredType closes the loop on the SQL
// side: every canonical type the derived set names must be REACHABLE from
// a real declared SQL type, and MapDeclaredType must report `known=false`
// for a type it does not map.
//
// Without the second half, MapDeclaredType could go back to answering
// TypeString for everything and CanonicalTypes() would be unchanged — the
// vocabulary would still look complete while nothing produced it.
func TestCanonicalTypes_MatchesMapDeclaredType(t *testing.T) {
	// One declared spelling per canonical type, written in SQL rather
	// than in canonical names, so this asserts the mapping and not the
	// table's agreement with itself.
	reachable := map[string]schemadef.CanonicalType{
		"TEXT":         schemadef.TypeString,
		"BIGINT":       schemadef.TypeInt,
		"NUMERIC(9,2)": schemadef.TypeFloat,
		"BOOLEAN":      schemadef.TypeBool,
		"TIMESTAMPTZ":  schemadef.TypeTime,
		"JSONB":        schemadef.TypeJSON,
		"BYTEA":        schemadef.TypeBytes,
	}

	got := map[schemadef.CanonicalType]bool{}
	for decl, want := range reachable {
		canonical, _, known := schemadef.MapDeclaredType(decl)
		if !known {
			t.Errorf("MapDeclaredType(%q) reported known=false", decl)
			continue
		}
		if canonical != want {
			t.Errorf("MapDeclaredType(%q) = %q, want %q", decl, canonical, want)
		}
		got[canonical] = true
	}

	for _, ct := range schemadef.CanonicalTypes() {
		if !got[ct] {
			t.Errorf("canonical type %q is in the derived vocabulary but no declared "+
				"SQL type in this test reaches it — either the mapping was removed "+
				"or this test needs the spelling that produces it", ct)
		}
	}

	// The refusal half: an unmapped SQL type must report known=false, not
	// arrive indistinguishable from a real TEXT column.
	for _, decl := range []string{"MONEY", "INET", "INTERVAL", "TSVECTOR", ""} {
		if _, _, known := schemadef.MapDeclaredType(decl); known {
			t.Errorf("MapDeclaredType(%q) reported known=true", decl)
		}
	}
}
