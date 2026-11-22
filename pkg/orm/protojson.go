package orm

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Proto values in json/jsonb columns.
//
// A `json`/`jsonb` column projects onto a Go `string` on the entity struct
// (see the generated internal/db row types), so the whole proto<->storage
// pairing for such a column is "which text goes in the string". The pairs
// below are that text, and the CRUD conversion generator emits calls to
// them for every wire field whose column is json/jsonb:
//
//	repeated <Message>  <->  a JSON array of objects   (…JSONBList)
//	<Message>           <->  a JSON object             (…JSONBMessage)
//	repeated <scalar>   <->  a JSON array of scalars   (…JSONBScalars)
//	map<K, scalar>      <->  a JSON object of scalars  (…JSONBMap)
//
// The two scalar-container pairs use encoding/json, because there is no
// proto message to apply proto JSON rules to — so an int64 inside them
// renders as a NUMBER, where an int64 inside a MESSAGE renders as a
// string. That difference is protojson's rule meeting Go's; it is stated
// here rather than smoothed over, because a SQL author reading the column
// will see it.
//
// Messages go through protojson, NOT encoding/json: protobuf-go structs
// carry unexported state, enums are declared VALUE NAMES rather than the
// wire numbers encoding/json would print, and google.protobuf.Timestamp is
// an RFC-3339 string rather than {"seconds":…,"nanos":…}. A column
// encoding/json wrote could not be read back through protojson, and the
// stored document would be unreadable to anything but Go.
//
// Two deliberate choices in the encoding, both visible in psql:
//
//   - UseProtoNames: keys are the proto field names (snake_case), matching
//     the column names around them, so `line_items -> 0 ->> 'product_id'`
//     is the spelling a SQL author expects. protojson's unmarshaler accepts
//     both spellings, so a hand-written or migrated document using
//     camelCase still reads.
//   - EmitUnpopulated: every declared field is present in the stored
//     document, so a SQL expression or a UI reading the raw column never
//     has to distinguish "absent key" from "zero value". proto3 has no
//     presence for plain scalars anyway — absence and zero are the same
//     value — so writing the key costs bytes and buys predictability.
//
// Unknown keys are an ERROR, never discarded: a document holding a field
// the current proto no longer declares is corrupt data (a rename that
// skipped a migration), and reading it back with the field silently
// dropped is the failure this pairing exists to prevent. It matches what
// the generated enum read path already does — a stored value name the
// proto no longer declares is a loud error, not a silent UNSPECIFIED.
//
// int64/uint64 render as JSON STRINGS ("3"), per canonical proto JSON.
// That is protojson's rule, not forge's; SQL `->>` yields the same text
// either way, and numeric comparison needs an explicit cast regardless.

// jsonbMarshal is the single encoder every proto->jsonb path uses.
var jsonbMarshal = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

// MarshalJSONBMessage renders one proto message as the text of a json/jsonb
// column. A nil message renders the message's ZERO document (every declared
// field at its zero value, per EmitUnpopulated), which is what proto3 says
// an absent message means and what the read path reconstructs — never
// SQL-invalid text, and never `null` where a NOT NULL column would reject
// it. Generated code storing into a NULLABLE column writes SQL NULL for a
// nil message instead and never calls this.
func MarshalJSONBMessage[T proto.Message](m T) (string, error) {
	b, err := jsonbMarshal.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("orm: marshal %T into a jsonb column: %w", m, err)
	}
	return string(b), nil
}

// MarshalJSONBList renders a repeated proto message field as the JSON array
// text of a json/jsonb column. A nil or empty slice renders "[]", never
// "null", so a column with CHECK (jsonb_typeof(col) = 'array') holds a legal
// value for an empty list.
func MarshalJSONBList[T proto.Message](list []T) (string, error) {
	if len(list) == 0 {
		return "[]", nil
	}
	parts := make([]json.RawMessage, 0, len(list))
	for i, m := range list {
		b, err := jsonbMarshal.Marshal(m)
		if err != nil {
			return "", fmt.Errorf("orm: marshal %T index %d into a jsonb column: %w", m, i, err)
		}
		parts = append(parts, b)
	}
	// Assembled with encoding/json over ALREADY-ENCODED elements: the
	// elements are protojson's output verbatim; this only puts the brackets
	// and commas around them.
	b, err := json.Marshal(parts)
	if err != nil {
		return "", fmt.Errorf("orm: assemble jsonb array: %w", err)
	}
	return string(b), nil
}

// MarshalJSONBScalars renders a repeated SCALAR wire field ([]string,
// []int64, …) as the JSON array text of a json/jsonb column. A nil or empty
// slice renders "[]" for the same reason MarshalJSONBList does. Message
// slices never come here — they need protojson (see MarshalJSONBList).
func MarshalJSONBScalars(list any) (string, error) {
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("orm: marshal %T into a jsonb column: %w", list, err)
	}
	if string(b) == "null" { // a nil slice
		return "[]", nil
	}
	return string(b), nil
}

// UnmarshalJSONBMessage parses a json/jsonb column's text into a singular
// proto message field. dst is the ADDRESS of the wire field (e.g.
// &m.Address, of type **pb.Address) so the message is allocated here only
// when the column holds one. Empty text and the JSON literal `null` leave
// dst untouched (nil) — that is how an absent message round-trips.
func UnmarshalJSONBMessage[T proto.Message](s string, dst *T) error {
	if isAbsentJSONB(s) {
		return nil
	}
	msg, err := newProtoMessage[T]()
	if err != nil {
		return err
	}
	if err := protojson.Unmarshal([]byte(s), msg); err != nil {
		return fmt.Errorf("orm: parse jsonb column into %T: %w", msg, err)
	}
	*dst = msg
	return nil
}

// UnmarshalJSONBList parses a json/jsonb column's array text into a repeated
// proto message field. dst is the ADDRESS of the wire field (e.g.
// &m.LineItems, of type *[]*pb.OrderLineItem). Empty text and `null` leave
// dst untouched (an empty list). A document that is not a JSON array is an
// error — the repeated field cannot represent it, and appending nothing
// would drop the row's data silently.
func UnmarshalJSONBList[T proto.Message](s string, dst *[]T) error {
	if isAbsentJSONB(s) {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return fmt.Errorf("orm: parse jsonb column as a JSON array: %w", err)
	}
	out := make([]T, 0, len(raw))
	for i, r := range raw {
		msg, err := newProtoMessage[T]()
		if err != nil {
			return err
		}
		if err := protojson.Unmarshal(r, msg); err != nil {
			return fmt.Errorf("orm: parse jsonb array index %d into %T: %w", i, msg, err)
		}
		out = append(out, msg)
	}
	*dst = out
	return nil
}

// UnmarshalJSONBScalars parses a json/jsonb column's array text into a
// repeated SCALAR wire field. dst is the ADDRESS of the field (e.g.
// &m.Tags, of type *[]string). Empty text and `null` leave dst untouched.
func UnmarshalJSONBScalars(s string, dst any) error {
	return unmarshalJSONBValue(s, dst)
}

// MarshalJSONBMap renders a proto map field with SCALAR values as the JSON
// object text of a json/jsonb column. A nil or empty map renders "{}" —
// the object shape its column expects, never "null". Maps of messages or
// enums never come here: encoding/json cannot encode either correctly, and
// entity birth TODO-skips them rather than creating a column.
func MarshalJSONBMap(m any) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("orm: marshal %T into a jsonb column: %w", m, err)
	}
	if string(b) == "null" { // a nil map
		return "{}", nil
	}
	return string(b), nil
}

// UnmarshalJSONBMap parses a json/jsonb column's object text into a proto
// map field with scalar values. dst is the ADDRESS of the field (e.g.
// &m.Labels, of type *map[string]string).
func UnmarshalJSONBMap(s string, dst any) error {
	return unmarshalJSONBValue(s, dst)
}

func unmarshalJSONBValue(s string, dst any) error {
	if isAbsentJSONB(s) {
		return nil
	}
	if err := json.Unmarshal([]byte(s), dst); err != nil {
		return fmt.Errorf("orm: parse jsonb column into %T: %w", dst, err)
	}
	return nil
}

// newProtoMessage allocates a T. T is always a generated message POINTER
// (*pb.Foo), whose zero value still answers ProtoReflect — that is how
// protobuf-go exposes a message's descriptor without an instance.
func newProtoMessage[T proto.Message]() (T, error) {
	var zero T
	msg, ok := zero.ProtoReflect().Type().New().Interface().(T)
	if !ok {
		return zero, fmt.Errorf("orm: %T is not an allocatable proto message", zero)
	}
	return msg, nil
}

// isAbsentJSONB reports the two texts that carry no value: the empty string
// (a column read back before anything was written, or a NULL scanned into a
// non-pointer Go string) and the JSON null literal.
func isAbsentJSONB(s string) bool { return s == "" || s == "null" }
