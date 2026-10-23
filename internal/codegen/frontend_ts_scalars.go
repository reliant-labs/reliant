package codegen

import "sort"

// One projection from a proto scalar kind to the TypeScript type
// protobuf-es declares for it — and the literals and conversions that
// projection implies.
//
// This is not forge's choice to make, exactly as ProtoTypeToGoType is not:
// the generated `_pb.ts` module already declares the field, and every
// frontend projection that disagrees with it emits TypeScript the
// project's own `tsc --noEmit` gate rejects — in files whose own header
// tells the author not to edit them.
//
// Before this table the frontend emitters answered the question three
// separate times: the mock TYPE switch (protoTypeToTSType), the mock
// VALUE switch (mockGenerateValue), and the form projection's
// bigint/numeric/repeated-numeric triplet. Every copy fell through to
// `string` for a kind it did not name, and the three disagreed with each
// other about which kinds those were. What that cost, measured on a
// virgin scaffold carrying one field of every kind:
//
//   - `bytes` mocked as a quoted string into a Uint8Array field (ten
//     rows × one error) and formed as `z.string()` fed straight into a
//     Uint8Array request field;
//   - `repeated bytes` mocked as string[] and formed as string[];
//   - `repeated bool` formed as string[] against boolean[];
//   - `repeated int64/uint64/sint64/fixed64/sfixed64` formed as
//     number[] against bigint[], because "repeated numeric" was a
//     two-way split (number or nothing) over a five-way truth.
//
// Nothing had ever put all fifteen kinds through the frontend path, so
// none of it was visible until an app declared a `bytes` column.

// protoScalarTS is the CLOSED proto scalar vocabulary — protobuf defines
// exactly these fifteen kinds — mapped to the TypeScript type
// protobuf-es v2 generates for a field of that kind.
//
// The 64-bit integers are `bigint` because that is protobuf-es's default
// jstype for them. A project can override the jstype to JS_STRING /
// JS_NORMAL in the proto; forge carries no signal for that today, and an
// override surfaces as a compile error naming the field rather than as
// silently wrong data.
var protoScalarTS = map[string]string{
	"string":   "string",
	"bool":     "boolean",
	"bytes":    "Uint8Array",
	"float":    "number",
	"double":   "number",
	"int32":    "number",
	"sint32":   "number",
	"sfixed32": "number",
	"uint32":   "number",
	"fixed32":  "number",
	"int64":    "bigint",
	"sint64":   "bigint",
	"sfixed64": "bigint",
	"uint64":   "bigint",
	"fixed64":  "bigint",
}

// protoScalarTSType returns the TypeScript type protobuf-es generates for
// a proto SCALAR kind. ok=false for anything else ("message", "enum",
// "map", a well-known type name) — callers branch on those before asking,
// and an unknown kind must never silently answer `string`, which is how
// nine kinds' worth of wrong TypeScript shipped.
func protoScalarTSType(kind string) (string, bool) {
	ts, ok := protoScalarTS[kind]
	return ts, ok
}

// ProtoScalarKinds returns the closed proto scalar vocabulary, sorted —
// the exact key set of the table above, never a restatement of it.
//
// It is exported for the end-to-end corpus, which has to answer a
// question no unit test can: which kinds actually travelled scaffold →
// generate → proto → TypeScript → tsc. That corpus previously
// carried a hand-written list of field types, which is a guard that
// cannot fail — it covers the kinds someone remembered, and `bytes` was
// not one of them, so a `bytes` column shipped green and produced 33 tsc
// errors in a generated frontend. Deriving the corpus's obligation from
// this table instead means a kind added here is either exercised
// end-to-end or fails a test by name.
func ProtoScalarKinds() []string {
	kinds := make([]string, 0, len(protoScalarTS))
	for kind := range protoScalarTS {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// isBigIntProtoType reports whether protobuf-es emits a TypeScript
// `bigint` (rather than `number`) field for the given proto scalar kind —
// the 64-bit integers. Mock data needs a `5n` / `BigInt("…")` literal for
// them and form submissions need a BigInt() conversion; a plain number
// produces "Type 'number' is not assignable to type 'bigint'".
func isBigIntProtoType(protoType string) bool {
	ts, ok := protoScalarTSType(protoType)
	return ok && ts == "bigint"
}

// isNumericProtoScalar reports whether a proto scalar kind lands on a
// JavaScript number tower (`number` or `bigint`) — the kinds an exact
// list filter renders as <input type="number"> and the kinds a `_cents`
// money column is allowed to be.
func isNumericProtoScalar(kind string) bool {
	ts, ok := protoScalarTSType(kind)
	return ok && (ts == "number" || ts == "bigint")
}

// tsFormControl returns the form control a scalar of the given TypeScript
// type is edited through.
//
// `bigint` is TEXT, not `<input type="number">`. An HTML number input is a
// JavaScript double, and a double cannot hold every int64: a row storing
// 9007199254740993 prefills the form as 9007199254740992, and saving
// without touching a thing writes that back. `uint64` max round-trips to
// 18446744073709551616, which the column then REJECTS. That is exactly the
// width loss dead263b refused on the Go side ("a cast that cannot lose a
// value in silence"), and the repeated case already proves text is the
// honest binding: a comma-separated `repeated int64` goes through
// BigInt(s) and round-trips exactly. The zod schema constrains the text to
// digits, so BigInt() below never sees anything else.
//
// `Uint8Array` has no native control that produces bytes, and base64 is
// the encoding the field ALREADY has on the wire (protojson encodes
// `bytes` as base64), so the scaffolded form edits it as base64 text —
// the one text encoding that can express every byte the column can hold.
// The emitted page says so and points at the swap for an upload column.
func tsFormControl(ts string) string {
	switch ts {
	case "boolean":
		return "checkbox"
	case "number":
		return "number"
	case "Uint8Array":
		return "base64"
	default:
		// string and bigint both bind to a text input; what separates them
		// is the zod schema and the submit conversion, not the control.
		return "text"
	}
}

// tsFromFormString returns the TypeScript expression converting one
// comma-split form STRING (bound to `s`) into an element of the given
// TypeScript type. ok=false means the pairing has no lossless conversion
// and the field must be kept OFF the form entirely (see
// repeatedScalarHasForm).
//
// It is the whole of what "repeated scalar" needs beyond the split: the
// form binds every repeated scalar to one comma-separated text input, so
// the element conversion is a pure function of the element's TS type.
// The predecessor was a bool named RepeatedNumeric that selected
// `.map(Number)` or nothing — a two-way answer to a five-way question,
// which is why `repeated int64` submitted number[] into bigint[].
//
// `boolean` is the refusal. Text → bool has no honest cast: every
// spelling a user would actually type — "1", "yes", "TRUE" — silently
// becomes false, and the form reports success. dead263b's rule for the
// same situation on the Go side was that family loss has no honest cast
// in EITHER direction, so the pairing is refused rather than papered
// over; a `repeated bool` gets no control, exactly as a repeated enum and
// a nested message get none.
func tsFromFormString(ts string) (string, bool) {
	switch ts {
	case "string":
		return "", true
	case "number":
		return "Number(s)", true
	case "bigint":
		return "BigInt(s)", true
	case "Uint8Array":
		return "base64Decode(s)", true
	default:
		return "", false
	}
}

// repeatedScalarHasForm reports whether a repeated field of the given
// element TypeScript type has a form control at all.
func repeatedScalarHasForm(ts string) bool {
	_, ok := tsFromFormString(ts)
	return ok
}

// tsToFormString returns the TypeScript expression rendering one element
// of the given TypeScript type (bound to `v`) back into the comma-joined
// text the repeated-scalar input holds, or "" when Array.prototype.join
// already produces it.
//
// Only `Uint8Array` needs one: joining a Uint8Array[] stringifies each
// element as its comma-separated byte values, which the submit handler's
// base64Decode cannot read back.
func tsToFormString(ts string) string {
	if ts == "Uint8Array" {
		return "base64Encode(v)"
	}
	return ""
}

// tsZodBase returns the zod schema expression a form field of the given
// TypeScript type validates through, and whether the field's projected
// protovalidate chain (ZodChain) may be appended to it.
//
// The chain is refused for the two encoded controls, and for the same
// reason in both: the zod value is a TEXT ENCODING of the field, not the
// field. A `bytes` rule's min_len counts BYTES while the value here is
// base64 (a third longer); an int64's gte/lte are numeric bounds while
// the value here is a digit string, where zod's .min() would measure
// LENGTH. Projecting either would reject values the wire accepts.
func tsZodBase(ts string) (expr string, allowChain bool) {
	switch ts {
	case "boolean":
		return "z.boolean().default(false)", false
	case "number":
		return "z.coerce.number()", true
	case "bigint":
		// Digits only — so BigInt() below is total, and so the exact
		// integer survives the round trip a number input would round off.
		// The empty string is admitted (BigInt("") is 0n, the proto3 zero)
		// and `.min(1, "Required")` is what makes a required field demand
		// one, exactly as it does for a string.
		return `z.string().regex(/^(-?\d+)?$/, "expected a whole number")`, false
	case "Uint8Array":
		return `z.string().regex(/^[A-Za-z0-9+/=_-]*$/, "expected base64")`, false
	default:
		return "z.string()", true
	}
}
