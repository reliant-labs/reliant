package codegen

// The proto scalar vocabulary is CLOSED — protobuf defines exactly fifteen
// scalar kinds — and `protoScalarTS` (frontend_ts_scalars.go) is the one
// place forge writes those fifteen names down. Everything keyed on that
// vocabulary derives from its key set rather than restating it: the raw
// .proto scanner deciding whether a type token is a scalar or a type name,
// the CRUD generator deciding whether a map value round-trips through
// encoding/json, the Go and TypeScript projections, the JSON Schema and
// KCL ones.
//
// Deriving rather than restating is not a style question. N copies of a
// closed set is N chances to disagree, and the disagreement is invisible in
// every copy read alone: a kind missing from one of them does not error, it
// takes that copy's permissive answer — a message type name, a `string`, an
// `object`, a `str` — which is indistinguishable from a correct answer for
// the kinds those really are.

// protoScalarGo is the CLOSED proto scalar vocabulary mapped to the Go type
// protoc-gen-go generates for a field of that kind.
//
// This is not forge's choice to make, exactly as protoScalarTS is not: the
// generated `pb` package already declares the field, and every projection
// that disagrees with it produces code that either refuses a legal shape or
// does not compile.
//
// Its key set is pinned equal to protoScalarTS's by
// TestProtoScalarGo_KeySetIsTheClosedVocabulary, so the Go half and the
// TypeScript half of one projection cannot drift apart — which is the
// failure dead263b found the expensive way, when `repeated bytes` resolved
// to []string on both halves and two wrong answers agreeing looked exactly
// like a mapped pairing.
var protoScalarGo = map[string]string{
	"string":   "string",
	"bool":     "bool",
	"bytes":    "[]byte",
	"float":    "float32",
	"double":   "float64",
	"int32":    "int32",
	"sint32":   "int32",
	"sfixed32": "int32",
	"uint32":   "uint32",
	"fixed32":  "uint32",
	"int64":    "int64",
	"sint64":   "int64",
	"sfixed64": "int64",
	"uint64":   "uint64",
	"fixed64":  "uint64",
}

// ProtoScalarGoType returns the Go type protoc-gen-go generates for a proto
// SCALAR kind. ok=false for anything else ("message", "enum", "map", a
// well-known type name) — callers branch on those before asking, and an
// unknown kind must never silently answer `string`, which is
// indistinguishable from the correct answer for the one kind that really is
// a string.
func ProtoScalarGoType(kind string) (string, bool) {
	goType, ok := protoScalarGo[kind]
	return goType, ok
}

// IsProtoScalarKind reports whether the token names one of the fifteen proto
// scalar kinds.
//
// It reads the closed table rather than carrying a set of its own, so the
// scanner's notion of "this token is a scalar, not a type name" and the
// projections' notion of "I have an answer for this kind" are the same
// notion. When they were two, a kind either table omitted was classified as
// a message type name by one and projected to `string` by the other, and
// neither said anything.
func IsProtoScalarKind(kind string) bool {
	_, ok := protoScalarTSType(kind)
	return ok
}
