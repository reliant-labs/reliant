package codegen

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The proto scalar vocabulary is CLOSED at fifteen kinds, and
// bornScalarShapes (scalar_shape_pairing_test.go) already holds it as a Go
// table for the Go half of the projection. The frontend half is asserted
// against that SAME table here, so a kind can never be added to one half
// and forgotten in the other: a new row in bornScalarShapes fails these
// tests until it has a TypeScript answer too.
//
// Nothing had ever put all fifteen kinds through the frontend path. What
// that hid, measured on a virgin scaffold carrying one field of each:
//
//   - `bytes` mocked as a quoted string into a Uint8Array field, and
//     formed as z.string() fed straight into a Uint8Array request field;
//   - `repeated bytes` mocked and formed as string[];
//   - `repeated bool` formed as string[] against boolean[];
//   - the five 64-bit integers, repeated, formed as number[] against
//     bigint[];
//   - `repeated enum` mocked as the number 1 against an enum[] and handed
//     to <StatusBadge value={…}>, whose prop is `string | number`.

// protobufESTSType is protobuf-es v2's OWN scalar mapping, restated here
// independently of protoScalarTS.
//
// The independence is the point and not pedantry: when `repeated bytes`
// resolved to []string on BOTH of forge's halves, every assertion that
// compared one half against the other passed while the emitted code did
// not compile (dead263b). An assertion that reads the projection it is
// checking proves only that the projection equals itself.
func protobufESTSType(kind string) (string, bool) {
	switch kind {
	case "string":
		return "string", true
	case "bool":
		return "boolean", true
	case "bytes":
		return "Uint8Array", true
	case "double", "float", "int32", "sint32", "sfixed32", "uint32", "fixed32":
		return "number", true
	case "int64", "sint64", "sfixed64", "uint64", "fixed64":
		// protobuf-es's default jstype for 64-bit integers.
		return "bigint", true
	}
	return "", false
}

// TestProtoScalarTS_IsTotalOverTheClosedVocabulary pins that the frontend
// TypeScript projection answers for EVERY proto scalar kind and for
// nothing else. A kind with no arm used to fall through to `string`,
// which is indistinguishable from a correct answer for the one kind that
// really is a string.
func TestProtoScalarTS_IsTotalOverTheClosedVocabulary(t *testing.T) {
	shapes := bornScalarShapes()
	if len(shapes) == 0 {
		t.Fatal("the born-scalar vocabulary is empty — this test would assert nothing and report green")
	}
	seen := map[string]bool{}
	for _, s := range shapes {
		seen[s.kind] = true
		want, known := protobufESTSType(s.kind)
		if !known {
			t.Fatalf("the proto scalar vocabulary grew %q and this test has no protobuf-es type for it — "+
				"add the arm to protobufESTSType (from protobuf-es, not from protoScalarTS)", s.kind)
		}
		got, ok := protoScalarTSType(s.kind)
		if !ok {
			t.Errorf("%s: no TypeScript projection — every frontend emitter falls back to `string` for it", s.kind)
			continue
		}
		if got != want {
			t.Errorf("protoScalarTSType(%q) = %q, want %q (protobuf-es's own type)", s.kind, got, want)
		}
		// The mock generator's entry point must agree, singular and
		// repeated — that is the projection the fixture literals are
		// checked against.
		if ts := protoTypeToTSType(s.kind); ts != want {
			t.Errorf("protoTypeToTSType(%q) = %q, want %q", s.kind, ts, want)
		}
		if ts := protoTypeToTSType("repeated " + s.kind); ts != want+"[]" {
			t.Errorf("protoTypeToTSType(\"repeated %s\") = %q, want %q", s.kind, ts, want+"[]")
		}
	}
	for kind := range protoScalarTS {
		if !seen[kind] {
			t.Errorf("protoScalarTS carries %q, which is not a proto scalar kind (bornScalarShapes is the vocabulary)", kind)
		}
	}
}

// tsLiteralPattern is the shape a TypeScript literal of each protobuf-es
// type must have. Written from TypeScript's own grammar: `1` is not a
// bigint, `"AQI="` is not a Uint8Array, and neither becomes one because a
// generator said so.
var tsLiteralPattern = map[string]*regexp.Regexp{
	"string":     regexp.MustCompile(`^".*"$`),
	"boolean":    regexp.MustCompile(`^(true|false)$`),
	"number":     regexp.MustCompile(`^-?\d+(\.\d+)?$`),
	"bigint":     regexp.MustCompile(`^(-?\d+n|BigInt\(".*"\))$`),
	"Uint8Array": regexp.MustCompile(`^new Uint8Array\(\[[\d, ]*\]\)$`),
}

// TestMockLiterals_TypeCheckAgainstProtobufES walks every proto scalar
// kind, singular and repeated, through the REAL mock value emitter and
// checks the literal it produces against the grammar of the type
// protobuf-es declares for that field.
//
// This is the assertion the emitted file could not make for itself: a
// `bytes` column mocked `"sample_barcode_3"` — a perfectly good string
// literal — into a Uint8Array field, ten rows at a time.
func TestMockLiterals_TypeCheckAgainstProtobufES(t *testing.T) {
	shapes := bornScalarShapes()
	if len(shapes) == 0 {
		t.Fatal("the born-scalar vocabulary is empty — this test would assert nothing and report green")
	}
	for _, s := range shapes {
		want, _ := protobufESTSType(s.kind)
		pattern := tsLiteralPattern[want]
		if pattern == nil {
			t.Fatalf("no literal grammar for TypeScript type %q (kind %s)", want, s.kind)
		}
		for _, rep := range []bool{false, true} {
			// The field name is deliberately plain: `id`/`*_id`/`name`
			// have their own value paths, and those are the subject of
			// frontend_mocks_pk_test.go.
			f := scalarWire("blob_"+s.kind, s, rep)
			ef := f
			ef.ProtoType = effectiveMockProtoType(f)
			got := mockGenerateValue(nil, "things", ef, 3, ServiceDef{})

			if !rep {
				if !pattern.MatchString(got) {
					t.Errorf("%s mocks %s, which is not a TypeScript %s", s.kind, got, want)
				}
				continue
			}
			elems, ok := splitTSArrayLiteral(got)
			if !ok {
				t.Errorf("repeated %s mocks %s, which is not an array literal (the field is %s[])", s.kind, got, want)
				continue
			}
			for _, e := range elems {
				if !pattern.MatchString(e) {
					t.Errorf("repeated %s mocks element %s, which is not a TypeScript %s", s.kind, e, want)
				}
			}
		}
	}
}

// splitTSArrayLiteral splits `[a, b]` into its top-level elements,
// respecting nested brackets and parens (a `new Uint8Array([1, 2])`
// element carries both).
func splitTSArrayLiteral(expr string) ([]string, bool) {
	e := strings.TrimSpace(expr)
	if !strings.HasPrefix(e, "[") || !strings.HasSuffix(e, "]") {
		return nil, false
	}
	inner := e[1 : len(e)-1]
	var out []string
	depth, start := 0, 0
	for i, r := range inner {
		switch r {
		case '[', '(':
			depth++
		case ']', ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(inner[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(inner[start:]); tail != "" {
		out = append(out, tail)
	}
	return out, true
}

// formProjection is what the form half must produce for a field of one
// protobuf-es TypeScript type: the zod expression that types the form
// value, the expression that turns that value into the WIRE value, and
// the expression that seeds the input from a fetched wire value.
//
// The three are stated together because they are one contract — zod
// declares the type the submit expression consumes and the prefill
// expression must produce — and the defect this pins was six independent
// copies of that contract in four page templates disagreeing with it.
//
// `<F>` stands in for the field name.
type formProjection struct {
	// control is the form control the page template renders — and for
	// bigint it is the whole assertion: an <input type="number"> is a
	// JavaScript double, so the control ALONE decides whether the value
	// survives the round trip, before any conversion runs.
	control string
	zod     string
	submit  string // "" = the ...values spread already carries the wire type
	prefill string
}

// wantSingularForm is the contract per TypeScript type.
//
// bigint is TEXT, not `z.coerce.number()`. An HTML number input is a
// double: a row holding 9007199254740993 prefills as 9007199254740992,
// and a Save that changed nothing writes that back — the width loss
// dead263b refused on the Go side. uint64's maximum comes back as
// 18446744073709551616, which the column then rejects outright.
var wantSingularForm = map[string]formProjection{
	"string":     {control: "text", zod: `z.string()`, prefill: `String(item.<F> ?? "")`},
	"boolean":    {control: "checkbox", zod: `z.boolean().default(false)`, prefill: `Boolean(item.<F>)`},
	"number":     {control: "number", zod: `z.coerce.number()`, prefill: `Number(item.<F> ?? 0)`},
	"bigint":     {control: "text", zod: `z.string().regex(/^(-?\d+)?$/, "expected a whole number")`, submit: `BigInt(values.<F>)`, prefill: `String(item.<F> ?? 0n)`},
	"Uint8Array": {control: "base64", zod: `z.string().regex(/^[A-Za-z0-9+/=_-]*$/, "expected base64")`, submit: `base64Decode(values.<F>)`, prefill: `base64Encode(item.<F> ?? new Uint8Array())`},
}

// emptyAsUnset is the preprocess a proto3 `optional` field's zod schema is
// wrapped in, so an empty input validates as absent rather than as the zero.
const emptyAsUnset = `z.preprocess((v) => (v === "" ? undefined : v), %s.optional())`

// wantOptionalForm is the same contract for a proto3 `optional` field: the
// zod value is `T | undefined`, the submit carries undefined through any
// conversion, and the prefill leaves a stored "absent" absent. A checkbox has
// no empty state, so `optional bool` keeps the singular contract.
var wantOptionalForm = map[string]formProjection{
	"string":     {control: "text", zod: fmt.Sprintf(emptyAsUnset, `z.string()`), prefill: `item.<F> ?? ""`},
	"boolean":    wantSingularForm["boolean"],
	"number":     {control: "number", zod: fmt.Sprintf(emptyAsUnset, `z.coerce.number()`), prefill: `item.<F>`},
	"bigint":     {control: "text", zod: fmt.Sprintf(emptyAsUnset, `z.string().regex(/^(-?\d+)?$/, "expected a whole number")`), submit: `values.<F> === undefined ? undefined : BigInt(values.<F>)`, prefill: `String(item.<F> ?? "")`},
	"Uint8Array": {control: "base64", zod: fmt.Sprintf(emptyAsUnset, `z.string().regex(/^[A-Za-z0-9+/=_-]*$/, "expected base64")`), submit: `values.<F> === undefined ? undefined : base64Decode(values.<F>)`, prefill: `item.<F> ? base64Encode(item.<F>) : ""`},
}

// wantRepeatedElemConv is the per-element conversion the comma-split
// submit expression must apply. A type absent from this map has NO form
// control at all: `boolean` is the refusal, because text → bool has no
// honest cast — "1", "yes" and "TRUE" all silently become false while the
// form reports success (dead263b's rule for a lossy pairing: refuse it).
var wantRepeatedElemConv = map[string]string{
	"string":     "",
	"number":     ".map((s) => Number(s))",
	"bigint":     ".map((s) => BigInt(s))",
	"Uint8Array": ".map((s) => base64Decode(s))",
}

// TestFormProjection_CoversEveryScalarKind walks the closed vocabulary
// through the REAL form projection, singular and repeated, and holds each
// field to the zod/submit/prefill contract for its protobuf-es type.
func TestFormProjection_CoversEveryScalarKind(t *testing.T) {
	shapes := bornScalarShapes()
	if len(shapes) == 0 {
		t.Fatal("the born-scalar vocabulary is empty — this test would assert nothing and report green")
	}
	for _, s := range shapes {
		ts, _ := protobufESTSType(s.kind)

		// No constraints are passed, so no field is required — required-ness
		// comes only from a rule — and the comparison stays about TYPES.
		name := "f_" + s.kind
		pf, ok := formPageField(ServiceDef{}, "Thing", formFieldDef{MessageFieldDef: MessageFieldDef{Name: name, ProtoType: s.kind}})
		if !ok {
			t.Errorf("%s: no form field — a scalar column the born form cannot set is a column the app cannot create", s.kind)
			continue
		}
		finalizePageField(&pf, nil)
		want := wantSingularForm[ts]
		camel := fieldNameToCamel(name)
		if pf.Type != want.control {
			t.Errorf("%s form control = %q, want %q", s.kind, pf.Type, want.control)
		}
		assertExpr(t, s.kind+" zod", pf.ZodExpr, want.zod, camel)
		assertExpr(t, s.kind+" submit", pf.SubmitExpr, want.submit, camel)
		assertExpr(t, s.kind+" prefill", pf.PrefillExpr, want.prefill, camel)

		// proto3 `optional`: an empty input is unset, never the zero.
		oname := "o_" + s.kind
		opf, ok := formPageField(ServiceDef{}, "Thing", formFieldDef{MessageFieldDef: MessageFieldDef{Name: oname, ProtoType: s.kind, IsOptional: true}})
		if !ok {
			t.Errorf("optional %s: no form field", s.kind)
			continue
		}
		finalizePageField(&opf, nil)
		owant := wantOptionalForm[ts]
		ocamel := fieldNameToCamel(oname)
		assertExpr(t, "optional "+s.kind+" zod", opf.ZodExpr, owant.zod, ocamel)
		assertExpr(t, "optional "+s.kind+" submit", opf.SubmitExpr, owant.submit, ocamel)
		assertExpr(t, "optional "+s.kind+" prefill", opf.PrefillExpr, owant.prefill, ocamel)

		// Repeated.
		rname := "r_" + s.kind
		rpf, ok := formPageField(ServiceDef{}, "Thing", formFieldDef{MessageFieldDef: MessageFieldDef{Name: rname, ProtoType: "[]" + s.kind}})
		conv, hasForm := wantRepeatedElemConv[ts]
		if !hasForm {
			if ok {
				t.Errorf("repeated %s got a form control; text → %s has no lossless conversion and the field must be excluded, "+
					"as a repeated enum and a nested message already are", s.kind, ts)
			}
			continue
		}
		if !ok {
			t.Errorf("repeated %s: no form field, but text → %s round-trips exactly", s.kind, ts)
			continue
		}
		finalizePageField(&rpf, nil)
		rcamel := fieldNameToCamel(rname)
		if rpf.Type != "text" {
			t.Errorf("repeated %s form control = %q, want \"text\" (one comma-separated input)", s.kind, rpf.Type)
		}
		assertExpr(t, "repeated "+s.kind+" zod", rpf.ZodExpr, "z.string()", rcamel)
		assertExpr(t, "repeated "+s.kind+" submit", rpf.SubmitExpr,
			`values.<F>.split(",").map((s) => s.trim()).filter(Boolean)`+conv, rcamel)
	}
}

func assertExpr(t *testing.T, label, got, want, field string) {
	t.Helper()
	want = strings.ReplaceAll(want, "<F>", field)
	if got != want {
		t.Errorf("%s = %s, want %s", label, quoteOrNone(got), quoteOrNone(want))
	}
}

func quoteOrNone(s string) string {
	if s == "" {
		return "(nothing — carried by the ...values spread)"
	}
	return fmt.Sprintf("`%s`", s)
}

// TestRepeatedEnum_IsNotProjectedAsASingularEnum pins the one kind whose
// `repeated` bit was dropped at the source.
//
// The message and scalar arms of schemaFieldToEntityField both record
// repeated-ness in GoType; the enum arm did not, so every consumer read a
// `repeated Status` field as a singular one. The frontend then mocked the
// number 1 into a Status[] and rendered <StatusBadge value={item.tags}>
// against a prop typed `string | number` — twelve of the fifty-seven
// TypeScript errors a full-vocabulary sweep produced, and the only ones a
// SCALAR table could never reach.
func TestRepeatedEnum_IsNotProjectedAsASingularEnum(t *testing.T) {
	const enumFQ = "demo.v1.Status"
	single := schemaFieldToEntityField(SchemaFieldDef{Name: "state", Kind: "enum", TypeName: enumFQ})
	repeated := schemaFieldToEntityField(SchemaFieldDef{Name: "tags", Kind: "enum", TypeName: enumFQ, Repeated: true})

	if isRepeatedEntityField(single) {
		t.Errorf("a singular enum reports repeated")
	}
	if !isRepeatedEntityField(repeated) {
		t.Errorf("a repeated enum reports singular — the descriptor's repeated bit is dropped")
	}

	// The mock literal must be an ARRAY of enum numbers, not one number.
	ef := repeated
	ef.ProtoType = effectiveMockProtoType(repeated)
	got := mockGenerateValue(nil, "things", ef, 1, ServiceDef{})
	if _, ok := splitTSArrayLiteral(got); !ok {
		t.Errorf("repeated enum mocks %s, which is not an array literal (the field is Status[])", got)
	}

	// And the column must NOT render as a badge: <StatusBadge value=…>
	// takes one `string | number`, so an array is a type error on both the
	// list and the detail page.
	page := PageTemplateData{}
	AttachEntityMeta(&page, EntityDef{
		Name:    "Thing",
		PkField: "id",
		Fields: []EntityField{
			schemaFieldToEntityField(SchemaFieldDef{Name: "id", Kind: "string"}),
			single, repeated,
		},
	}, ServiceDef{})
	for _, c := range page.Columns {
		switch c.Name {
		case "state":
			if !c.IsBadge {
				t.Errorf("the singular enum column stopped rendering as a badge")
			}
		case "tags":
			if c.IsBadge || c.EnumType != "" {
				t.Errorf("the repeated enum column renders as a badge (IsBadge=%v EnumType=%q); "+
					"StatusBadge's value prop is `string | number`", c.IsBadge, c.EnumType)
			}
		}
	}
}
