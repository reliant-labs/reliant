package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// wireInt64 decodes a protobuf 64-bit integer from Connect's JSON codec.
//
// protojson encodes int64 / uint64 / fixed64 / sfixed64 as a JSON **string**,
// because a 64-bit integer does not survive IEEE-754 double precision. int32
// and narrower stay bare numbers, which is why only the 64-bit fields are
// affected. Decoding one into a plain `int64` fails the WHOLE document:
//
//	json: cannot unmarshal string into Go struct field .rollout.stabilityWindowMs of type int64
//
// so a single unreadable field discards every other field beside it. Measured:
// `forge env deploy <env> <version>` hung for its entire 15m wait budget
// retrying GetRollout on exactly that error while the rollout was healthy.
//
// Declared HERE as well as in internal/deploytarget, rather than shared from
// one package: deploytarget keeps a deliberate independence from this package
// (see the header of deploytarget/hosted.go), and forge's own guidance is to
// declare a type at its consumer and prefer two small local copies to one
// exported coupling.
type wireInt64 int64

// UnmarshalJSON accepts a JSON string (protojson's encoding), a JSON number,
// or null.
func (v *wireInt64) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
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
