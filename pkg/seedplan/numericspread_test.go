package seedplan

import (
	"strconv"
	"strings"
	"testing"
)

// A declared {min, max} must expand to a pool that SPANS the declaration, not
// to its first numericRangeMaxValues integers.
//
// The defect this pins: expand() walked `v += step` with step defaulting to 1
// and stopped at the 512-value cap, so a range whose width exceeds the cap was
// truncated at the FLOOR — `{min: 20480, max: 52428800}` became
// [20480…20991], 0.001% of the declared span, and the declared max was
// unreachable by construction. It was invisible for a narrow range (481 values
// fit under the cap) and total for a wide one, which is why the symptom read
// as magnitude-sensitive.
//
// The assertion is on the observed span as a FRACTION of the declared span
// rather than on exact members: the cap is a real constraint (a range wide
// enough to exhaust memory is an authoring mistake, not a seed), so the fix is
// to stride across the range instead of walking it, and a stride's exact
// members are an implementation detail while its coverage is the contract.
func TestNumericRange_WideRangeSpansTheDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		min, max float64
	}{
		// The dogfooded case: a file-size column three orders of magnitude
		// wider than the expansion cap.
		{"documents.size_bytes", `{min: 20480, max: 52428800}`, 20480, 52428800},
		// Wider still, and the second column the same run reported.
		{"organizations.storage_quota_bytes", `{min: 1073741824, max: 107374182400}`, 1073741824, 107374182400},
		// The CONTROL: narrower than the cap, already correct before the fix.
		// It is here so a future change cannot spread the wide range by
		// breaking the narrow one.
		{"share_links.view_count", `{min: 0, max: 480}`, 0, 480},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeVocab(t, "columns:\n  t."+tc.name+": "+tc.body+"\n")
			v, err := LoadVocab(path)
			if err != nil {
				t.Fatalf("LoadVocab: %v", err)
			}
			pool := v.Columns["t."+tc.name]
			if len(pool) == 0 {
				t.Fatal("range produced no values")
			}
			lo, hi := poolExtent(t, pool)

			// Nothing may fall outside the declaration — the guarantee the
			// previous fix established, which this one must not regress.
			if lo < tc.min || hi > tc.max {
				t.Errorf("pool [%v,%v] escapes the declared range [%v,%v]", lo, hi, tc.min, tc.max)
			}
			// And the pool must actually COVER the declaration. 90% leaves
			// room for a stride that cannot land exactly on both endpoints
			// while still failing every floor-hugging draw by three orders of
			// magnitude.
			declared := tc.max - tc.min
			observed := hi - lo
			if frac := observed / declared; frac < 0.9 {
				t.Errorf("observed span %v of declared span %v = %.4f%% — want >=90%%; pool is [%v,%v] over %d values",
					observed, declared, frac*100, lo, hi, len(pool))
			}
			if len(pool) > numericRangeMaxValues {
				t.Errorf("pool has %d values, above the %d cap", len(pool), numericRangeMaxValues)
			}
		})
	}
}

// A declared step too fine to cover its range within the cap is the one case
// where the author's two statements cannot both hold. The range wins (a
// clustered pool is the useless dataset this whole fix exists to prevent) and
// the widening is NAMED, in the same voice as the seeder's other refusals —
// silently producing either a truncated pool or a different granularity is
// what sent two dogfood runs looking for a seeding workaround.
func TestNumericRange_UnsatisfiableStepIsWidenedAndNamed(t *testing.T) {
	path := writeVocab(t, "columns:\n  documents.size_bytes: {min: 20480, max: 52428800, step: 1}\n")
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	pool := v.Columns["documents.size_bytes"]
	lo, hi := poolExtent(t, pool)
	if frac := (hi - lo) / (52428800 - 20480); frac < 0.9 {
		t.Errorf("observed span fraction %.4f%%, want >=90%% — pool [%v,%v]", frac*100, lo, hi)
	}
	if len(v.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one naming the widened step", v.Warnings)
	}
	for _, want := range []string{"documents.size_bytes", "step"} {
		if !strings.Contains(v.Warnings[0], want) {
			t.Errorf("warning %q does not mention %q", v.Warnings[0], want)
		}
	}
}

// A wide fractional range with no declared step strides in the column's own
// precision and still reaches the top of the declaration.
func TestNumericRange_WideDecimalsSpanTheDeclaration(t *testing.T) {
	path := writeVocab(t, "columns:\n  invoices.amount: {min: 500.00, max: 29000.00, decimals: 2}\n")
	v, err := LoadVocab(path)
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	pool := v.Columns["invoices.amount"]
	lo, hi := poolExtent(t, pool)
	if lo < 500 || hi > 29000 {
		t.Errorf("pool [%v,%v] escapes the declared range [500,29000]", lo, hi)
	}
	if frac := (hi - lo) / (29000 - 500); frac < 0.9 {
		t.Errorf("observed span fraction %.4f%%, want >=90%% — pool [%v,%v]", frac*100, lo, hi)
	}
	for _, m := range pool {
		if _, err := strconv.ParseFloat(m, 64); err != nil {
			t.Fatalf("pool member %q does not parse: %v", m, err)
		}
	}
}

// A range spanning most of the int64 domain must TERMINATE and stay under the
// cap. It is a nonsense declaration, but `max - min` overflows to a negative
// span there, which skips the stride and — with no count bound — walks the
// range one unit at a time forever. The loop's termination must not depend on
// the arithmetic being well-behaved, so this pins it.
func TestNumericRange_OverflowingSpanTerminates(t *testing.T) {
	for _, r := range []numericRange{
		{min: -9000000000000000000, max: 9000000000000000000, step: 1},
		{min: -9223372036854775808, max: 9223372036854775807, step: 1},
	} {
		values, _ := r.expand()
		if len(values) == 0 {
			t.Fatalf("range [%d,%d] produced no values", r.min, r.max)
		}
		if len(values) > numericRangeMaxValues {
			t.Errorf("range [%d,%d] produced %d values, above the %d cap",
				r.min, r.max, len(values), numericRangeMaxValues)
		}
	}
}

// The degenerate shapes stay well-defined: a point range is one value, and a
// step wider than the range yields just the floor.
func TestNumericRange_DegenerateShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		rng  numericRange
		want []string
	}{
		"min equals max": {numericRange{min: 5, max: 5, step: 1}, []string{"5"}},
		"step over span": {numericRange{min: 1, max: 3, step: 10}, []string{"1"}},
		"negative range": {numericRange{min: -3, max: -1, step: 1}, []string{"-3", "-2", "-1"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := tc.rng.expand()
			if !equal(got, tc.want) {
				t.Errorf("expand() = %v, want %v", got, tc.want)
			}
		})
	}
}

// poolExtent returns the lowest and highest numeric value in a pool.
func poolExtent(t *testing.T, pool []string) (lo, hi float64) {
	t.Helper()
	for i, m := range pool {
		v, err := strconv.ParseFloat(m, 64)
		if err != nil {
			t.Fatalf("pool member %q does not parse: %v", m, err)
		}
		if i == 0 || v < lo {
			lo = v
		}
		if i == 0 || v > hi {
			hi = v
		}
	}
	return lo, hi
}
