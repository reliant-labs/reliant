package seedplan

import (
	"reflect"
	"testing"
)

// A one-member vocabulary is `col = 'x'::text` in the catalog, not an ANY
// list. Missing it leaves the column poolless, so the seeder invents a value
// the CHECK rejects.
func TestPoolFromCheckDef_SingleLiteralEquality(t *testing.T) {
	tests := []struct {
		name string
		def  string
		want []string
	}{
		{"single text literal", `CHECK ((provider = 'byo'::text))`, []string{"byo"}},
		{"bare literal", `CHECK ((provider = 'byo'))`, []string{"byo"}},
		{"escaped quote", `CHECK ((name = 'it''s'::text))`, []string{"it's"}},
		{"still an ANY list", `CHECK ((p = ANY (ARRAY['a'::text, 'b'::text])))`, []string{"a", "b"}},
		{"conjunction is not a pool", `CHECK (((a = 'x'::text) AND (b > 1)))`, nil},
		{"function call is not a pool", `CHECK ((lower(a) = 'x'::text))`, nil},
		{"numeric comparison", `CHECK ((n >= 100))`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := poolFromCheckDef(tc.def)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("poolFromCheckDef(%q) = %v, want %v", tc.def, got, tc.want)
			}
		})
	}
}
