package deploytarget

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// wireInt64 decodes a protobuf 64-bit integer from Connect's JSON codec.
//
// WHY THIS TYPE EXISTS. protojson encodes int64 / uint64 / fixed64 / sfixed64
// as a JSON **string**, not a number — deliberately, because a 64-bit integer
// does not survive IEEE-754 double precision and several JSON parsers would
// silently round it. (The same spec leaves int32 and friends as bare numbers,
// which is why the replica counts beside this field decode into plain int32
// and always worked.)
//
// So a plain `int64` field is the wrong decode target for ANY 64-bit proto
// scalar coming back from the control plane, and the failure is total rather
// than partial: encoding/json rejects the whole document with
//
//	json: cannot unmarshal string into Go struct field .rollout.stabilityWindowMs of type int64
//
// which means one unreadable field takes the entire response with it.
//
// MEASURED, NOT HYPOTHETICAL. `forge env deploy <env> <version>` against a
// hosted env hung for its full 15m wait budget, retrying GetRollout on exactly
// that error, printed "Rollout … UNKNOWN", and then told the operator to run
// the deploy that had in fact already recorded the binding. The rollout was
// healthy the whole time; forge simply could not read the answer.
//
// Accepting BOTH forms is the point: a number is what forge's own fixtures and
// any hand-written JSON produce, a string is what a real protojson server
// sends, and a field absent or null stays the zero value. That keeps this
// compatible with every control plane rather than only the current one.
type wireInt64 int64

// UnmarshalJSON accepts a JSON string (protojson's encoding), a JSON number
// (hand-written or non-protojson producers), or null.
func (v *wireInt64) UnmarshalJSON(b []byte) error {
	if s := string(b); s == "null" {
		return nil
	}
	// A quoted value is protojson's 64-bit encoding; unquote, then parse the
	// same way a bare number is parsed, so both paths agree on what is valid.
	raw := string(b)
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		raw = s
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("decode 64-bit integer from %s: %w", b, err)
	}
	*v = wireInt64(n)
	return nil
}

// Int64 is the decoded value.
func (v wireInt64) Int64() int64 { return int64(v) }
