package wfcel

import (
	"math"
	"testing"
)

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

// A double holds every integer only up to 2^53: within that range the int
// overload is exact, and beyond it is an error, never a different number.
func TestMathRoundingIntsBeyondExactDoubleRangeFail(t *testing.T) {
	const exact = int64(1) << 53
	for _, n := range []int64{exact, -exact, exact - 1, -exact + 1} {
		out, err := EvaluateValue("math.trunc(inputs.n)", &EdgeEvalContext{Inputs: map[string]interface{}{"n": n}})
		if err != nil {
			t.Fatalf("math.trunc(%d): %v", n, err)
		}
		if got, ok := out.(float64); !ok || int64(got) != n {
			t.Fatalf("math.trunc(%d) = %#v, want exactly %d", n, out, n)
		}
	}
	for _, n := range []int64{exact + 1, -exact - 1, math.MaxInt64, math.MinInt64} {
		for _, fn := range []string{"ceil", "floor", "round", "trunc"} {
			expr := "math." + fn + "(inputs.n)"
			if out, err := EvaluateValue(expr, &EdgeEvalContext{Inputs: map[string]interface{}{"n": n}}); err == nil {
				t.Fatalf("%s with n=%d = %#v, want an error (no exact double)", expr, n, out)
			}
		}
	}
}
