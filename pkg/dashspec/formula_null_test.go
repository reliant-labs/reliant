package dashspec

import "testing"

func TestEvalNullable(t *testing.T) {
	for _, tc := range []struct {
		expr    string
		vals    []float64
		present []bool
		want    float64
		ok      bool
	}{
		{"A / B", []float64{1, 4}, []bool{true, true}, 0.25, true},
		{"A / B", []float64{1, 4}, []bool{true, false}, 0, false},
		{"A / B", []float64{1, 0}, []bool{true, true}, 0, false},
		{"A + 1", []float64{1}, []bool{false}, 0, false},
		{"-A", []float64{2}, []bool{true}, -2, true},
		{"A * (B - 1)", []float64{3, 3}, []bool{true, true}, 6, true},
		{"A - B", []float64{3, 1}, []bool{true, true}, 2, true},
		{"2 * 3", nil, nil, 0, false}, // no refs: the parser refuses it before this
	} {
		f, err := ParseFormula(tc.expr, 2)
		if err != nil {
			if tc.expr == "2 * 3" {
				continue
			}
			t.Fatalf("%s: %v", tc.expr, err)
		}
		got, ok := f.EvalNullable(tc.vals, tc.present)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s %v %v = %v, %v; want %v, %v", tc.expr, tc.vals, tc.present, got, ok, tc.want, tc.ok)
		}
	}
}
