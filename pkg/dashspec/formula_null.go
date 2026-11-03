package dashspec

import "math"

// EvalNullable computes the formula where a series' value may be absent:
// present[i] says whether vals[i] exists. Absence propagates (an expression with
// an absent operand is absent), a division by zero is absent, and so is any
// non-finite result. A reader draws an absent result as a gap, which is what a
// gauge ratio wants where Eval's 0 would be a lie.
func (f *Formula) EvalNullable(vals []float64, present []bool) (float64, bool) {
	v, ok := evalNullable(f.root, vals, present)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func evalNullable(n formulaNode, vals []float64, present []bool) (float64, bool) {
	switch n := n.(type) {
	case numNode:
		return n.v, true
	case refNode:
		if int(n) < len(vals) && int(n) < len(present) && present[n] {
			return vals[n], true
		}
		return 0, false
	case negNode:
		v, ok := evalNullable(n.x, vals, present)
		return -v, ok
	case binNode:
		l, lok := evalNullable(n.l, vals, present)
		r, rok := evalNullable(n.r, vals, present)
		if !lok || !rok {
			return 0, false
		}
		switch n.op {
		case '+':
			return l + r, true
		case '-':
			return l - r, true
		case '*':
			return l * r, true
		}
		if r == 0 {
			return 0, false
		}
		return l / r, true
	}
	return 0, false
}
