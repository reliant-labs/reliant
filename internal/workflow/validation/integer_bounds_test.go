package validation

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegerFloatBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		valid bool
	}{
		{"minimum", float64(-0x1p63), true},
		{"below minimum", math.Nextafter(-0x1p63, math.Inf(-1)), false},
		{"below upper bound", math.Nextafter(0x1p63, 0), true},
		{"upper bound exclusive", float64(0x1p63), false},
		{"float32 overflow", float32(0x1p63), false},
		{"nan", math.NaN(), false},
		{"positive infinity", math.Inf(1), false},
		{"negative infinity", math.Inf(-1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := InputValue(tc.value, integerInput(nil))
			require.Equal(t, tc.valid, ok)
			if tc.valid {
				require.IsType(t, int64(0), got)
			} else if v, isFloat := tc.value.(float64); isFloat && math.IsNaN(v) {
				require.True(t, math.IsNaN(got.(float64)))
			} else {
				require.Equal(t, tc.value, got, "invalid input must not mutate")
			}
		})
	}
}
