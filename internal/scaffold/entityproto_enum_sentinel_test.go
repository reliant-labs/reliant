// File: internal/scaffold/entityproto_enum_sentinel_test.go
//
// ONE question, asked of every surface that answers it: is the proto3 zero
// sentinel a value a row may hold?
//
// forge used to answer it four different ways for the same column. The
// birth comment said the sentinel "means unset, never a state a row is
// in"; the scaffolded create form refused to submit it; the CHECK
// constraint two lines under that comment ADMITTED it; and the NOT NULL
// DEFAULT named a real member, implying the sentinel was not what an
// omitted value meant. An author who followed forge's own comment and
// dropped the sentinel from the CHECK was then told by `forge generate`
// to put it back.
//
// The answer is no, and this file pins it at the emitter: proto3 gives a
// plain enum field NO presence — a field left unset and a field set to the
// zero are byte-identical on the wire — so the zero value exists to mean
// "the caller did not say". That is a fact about a REQUEST, never a fact
// about a stored row, and a column that admits it admits a non-state every
// reader then has to handle.
package scaffold

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// enumSpec renders a one-enum-field entity so a test can read the emitted
// column line for that field.
func enumSpec(t *testing.T, field codegen.SchemaFieldDef, values []string) string {
	t.Helper()
	mig := RenderEntityMigrationFromProto(EntityFromProtoSpec{
		Table:     "orders",
		MessageFQ: entityProtoPkg + ".Order",
		ProtoPkg:  entityProtoPkg,
		Fields:    []codegen.SchemaFieldDef{field},
		Enums:     map[string][]string{entityProtoPkg + ".OrderStatus": values},
	})
	return mig.UpSQL
}

// TestBornEnumCheck_RefusesTheProtoZeroSentinel is the RED→GREEN guard:
// the CHECK vocabulary must not admit the sentinel the same migration's
// comment calls "unset", for either cardinality that gets a CHECK.
func TestBornEnumCheck_RefusesTheProtoZeroSentinel(t *testing.T) {
	sentinel := "ORDER_STATUS_UNSPECIFIED"
	values := []string{sentinel, "ORDER_STATUS_OPEN", "ORDER_STATUS_CLOSED"}

	for _, tc := range []struct {
		name     string
		field    codegen.SchemaFieldDef
		wantLine string
	}{
		{
			name:  "not null",
			field: codegen.SchemaFieldDef{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus"},
			wantLine: "status TEXT NOT NULL DEFAULT 'ORDER_STATUS_OPEN' " +
				"CHECK (status IN ('ORDER_STATUS_OPEN', 'ORDER_STATUS_CLOSED'))",
		},
		{
			// A nullable enum column already has a spelling for "unset" —
			// NULL — so the sentinel is redundant there too.
			name:     "optional",
			field:    codegen.SchemaFieldDef{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus", Optional: true},
			wantLine: "status TEXT CHECK (status IN ('ORDER_STATUS_OPEN', 'ORDER_STATUS_CLOSED'))",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := enumSpec(t, tc.field, values)
			if !strings.Contains(up, "    "+tc.wantLine) {
				t.Errorf("column line\n  want: %s\n  got:\n%s", tc.wantLine, up)
			}
			// The sentinel may still be NAMED in the explanatory comment —
			// what it may not be is inside the CHECK's value list.
			for _, ln := range strings.Split(up, "\n") {
				if strings.HasPrefix(strings.TrimSpace(ln), "--") {
					continue
				}
				if strings.Contains(ln, "CHECK (") && strings.Contains(ln, sentinel) {
					t.Errorf("CHECK admits the proto zero sentinel: %s", strings.TrimSpace(ln))
				}
			}
		})
	}
}

// TestBornEnumCheck_KeepsAZeroThatIsARealState guards the other direction:
// the rule is about the SENTINEL, not about "index 0". An enum whose zero
// member is a real domain state (no _UNSPECIFIED/_UNKNOWN naming) has no
// spelling for "unset" at all, so every member stays in the vocabulary —
// dropping one would delete a value the domain has.
func TestBornEnumCheck_KeepsAZeroThatIsARealState(t *testing.T) {
	up := enumSpec(t,
		codegen.SchemaFieldDef{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus"},
		[]string{"ORDER_STATUS_OPEN", "ORDER_STATUS_CLOSED"})
	want := "status TEXT NOT NULL DEFAULT 'ORDER_STATUS_OPEN' " +
		"CHECK (status IN ('ORDER_STATUS_OPEN', 'ORDER_STATUS_CLOSED'))"
	if !strings.Contains(up, "    "+want) {
		t.Errorf("column line\n  want: %s\n  got:\n%s", want, up)
	}
}

// TestBornEnumCheck_MidListUnknownSurvives is the reason the vocabulary is
// derived positionally rather than by re-filtering every member by name: a
// member NAMED like a sentinel but declared at a non-zero number is a real
// state (someone's "we asked and the answer is unknown"), and only the
// number-zero member carries proto3's no-presence meaning.
func TestBornEnumCheck_MidListUnknownSurvives(t *testing.T) {
	up := enumSpec(t,
		codegen.SchemaFieldDef{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus"},
		[]string{"ORDER_STATUS_UNSPECIFIED", "ORDER_STATUS_OPEN", "ORDER_STATUS_UNKNOWN"})
	if !strings.Contains(up, "CHECK (status IN ('ORDER_STATUS_OPEN', 'ORDER_STATUS_UNKNOWN'))") {
		t.Errorf("a non-zero member named _UNKNOWN is a real state and must stay in the vocabulary; got:\n%s", up)
	}
}

// TestBornEnumCheck_SentinelOnlyEnumStaysApplyable: an enum declaring
// nothing but its sentinel has no real member to fall back to. Emitting
// `CHECK (status IN ())` would be un-applyable SQL, so the degenerate case
// keeps the sentinel — and the DEFAULT agrees with it.
func TestBornEnumCheck_SentinelOnlyEnumStaysApplyable(t *testing.T) {
	up := enumSpec(t,
		codegen.SchemaFieldDef{Name: "status", Kind: "enum", TypeName: entityProtoPkg + ".OrderStatus"},
		[]string{"ORDER_STATUS_UNSPECIFIED"})
	want := "status TEXT NOT NULL DEFAULT 'ORDER_STATUS_UNSPECIFIED' " +
		"CHECK (status IN ('ORDER_STATUS_UNSPECIFIED'))"
	if !strings.Contains(up, "    "+want) {
		t.Errorf("column line\n  want: %s\n  got:\n%s", want, up)
	}
}
