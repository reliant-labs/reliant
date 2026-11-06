package wfcel

import "testing"

// A whole number that crossed a node boundary is an int; rounding it must not
// fail for want of an overload, and must read the same as the double form.
func TestMathRoundingAcceptsInts(t *testing.T) {
	inputs := map[string]interface{}{"total": int64(1500), "ratio": 2.5}
	for expr, want := range map[string]float64{
		"math.round(inputs.total)": 1500,
		"math.ceil(inputs.total)":  1500,
		"math.floor(inputs.total)": 1500,
		"math.trunc(inputs.total)": 1500,
		"math.round(inputs.ratio)": 3,
		"math.round(7)":            7,
	} {
		out, err := EvaluateValue(expr, &EdgeEvalContext{Inputs: inputs})
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got, ok := out.(float64); !ok || got != want {
			t.Fatalf("%s = %#v, want %v", expr, out, want)
		}
	}
}
