package changes

import "encoding/json"

// The typed accessors turn a Result into the value a call site uses, with the
// default written in code. A result with no value (the caller's default
// applies) or a value of another JSON type gives def, so a snapshot can never
// hand a call site a value of the wrong type.
//
//	on := changes.Bool(store.Evaluate(ctx, "new-checkout", ec), false)

// Bool is r's value when it is a JSON boolean, else def.
func Bool(r Result, def bool) bool {
	var v bool
	if !decodeValue(r, &v) {
		return def
	}
	return v
}

// String is r's value when it is a JSON string, else def.
func String(r Result, def string) string {
	var v string
	if !decodeValue(r, &v) {
		return def
	}
	return v
}

// Float64 is r's value when it is a JSON number, else def.
func Float64(r Result, def float64) float64 {
	var v float64
	if !decodeValue(r, &v) {
		return def
	}
	return v
}

// Decode unmarshals r's value into v, as json.Unmarshal does, for a change
// whose variants are JSON objects or arrays. It reports false when the
// caller's default applies (v is untouched) or the value does not fit v (v
// may be partly written, as json.Unmarshal leaves it), so decode into a fresh
// value and fall back to the default on false:
//
//	var limits Limits
//	if !changes.Decode(store.Evaluate(ctx, "upload-limits", ec), &limits) {
//		limits = defaultLimits
//	}
func Decode(r Result, v any) bool {
	return decodeValue(r, v)
}

// decodeValue unmarshals r's value into v, reporting false when there is
// none. A variant value is never JSON null, so null never reaches here.
func decodeValue(r Result, v any) bool {
	return len(r.Value) > 0 && json.Unmarshal(r.Value, v) == nil
}
