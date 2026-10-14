package wfcel

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// MaxJSONDecodeBytes bounds the text parseJson and json.decode will read.
// Decoding is one function call to CEL's cost model, so the bound has to be
// enforced here; a run node's whole stdout is the realistic input.
const MaxJSONDecodeBytes = 8 << 20

// decodeJSON parses exactly one JSON value. Numbers written as integers become
// int64 and everything else float64: a GitHub id such as 225976313 must stay
// 225976313 under string(), where a float64 renders as 2.25976313e+08.
func decodeJSON(text string) (any, error) {
	if len(text) > MaxJSONDecodeBytes {
		return nil, fmt.Errorf("JSON text is %d bytes; the limit is %d", len(text), MaxJSONDecodeBytes)
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return normalizeJSONNumbers(out), nil
}

func normalizeJSONNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if !strings.ContainsAny(s, ".eE") {
			if i, err := t.Int64(); err == nil {
				return i
			}
		}
		f, _ := t.Float64()
		return f
	case []any:
		for i := range t {
			t[i] = normalizeJSONNumbers(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = normalizeJSONNumbers(t[k])
		}
		return t
	default:
		return v
	}
}

// celJSONDecodeFunction is json.decode(string) -> optional<dyn>: the parsed
// value, or optional.none() when the text is empty or is not JSON. A node's
// stdout is often "not JSON yet" (a tool printed an error instead); this reads
// it without failing the step:
//
//	json.decode(nodes.start.stdout).?status.orValue('error')
//	json.decode(nodes.start.stdout).hasValue()
func celJSONDecodeFunction() cel.EnvOption {
	return cel.Function("json.decode",
		cel.Overload("json_decode_string", []*cel.Type{cel.StringType}, cel.OptionalType(cel.DynType),
			cel.UnaryBinding(func(val ref.Val) ref.Val {
				text, ok := val.Value().(string)
				if !ok {
					return types.MaybeNoSuchOverloadErr(val)
				}
				if strings.TrimSpace(text) == "" {
					return types.OptionalNone
				}
				if len(text) > MaxJSONDecodeBytes {
					return types.NewErr("json.decode(): JSON text is %d bytes; the limit is %d", len(text), MaxJSONDecodeBytes)
				}
				decoded, err := decodeJSON(text)
				if err != nil {
					return types.OptionalNone
				}
				return types.OptionalOf(types.DefaultTypeAdapter.NativeToValue(decoded))
			}),
		),
	)
}

// costEstimator charges the functions whose work scales with their argument
// by that size; CEL's default is a flat 1 per call, which would let one call
// parse megabytes for free.
type costEstimator struct{}

func (costEstimator) CallCost(function, _ string, args []ref.Val, result ref.Val) *uint64 {
	var n int
	switch function {
	case "parseJson", "json.decode":
		if len(args) == 1 {
			if s, ok := args[0].Value().(string); ok {
				n = len(s)
			}
		}
	case "toJson":
		if s, ok := result.Value().(string); ok {
			n = len(s)
		}
	default:
		return nil
	}
	c := uint64(n/16) + 1
	return &c
}
