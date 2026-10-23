package orm

import (
	"reflect"
	"testing"
)

// TestEmptyIfNil_NilBecomesEmpty pins the NOT NULL array-column class: a
// nil repeated field must bind a non-nil empty slice so Bun renders the
// empty postgres array (`{}`), never NULL — array columns are
// conventionally `NOT NULL DEFAULT '{}'`, and binding NULL violates the
// constraint and fails every INSERT that omits the optional slice.
func TestEmptyIfNil_NilBecomesEmpty(t *testing.T) {
	var nilStrings []string
	if got := EmptyIfNil(nilStrings); got == nil || len(got) != 0 {
		t.Errorf("EmptyIfNil(nil []string) = %v (nil=%v), want non-nil empty slice", got, got == nil)
	}
	var nilInts []int64
	if got := EmptyIfNil(nilInts); got == nil || len(got) != 0 {
		t.Errorf("EmptyIfNil(nil []int64) = %v (nil=%v), want non-nil empty slice", got, got == nil)
	}
	// Non-nil slices are passed through unchanged.
	in := []string{"a", "b"}
	if got := EmptyIfNil(in); !reflect.DeepEqual(got, in) {
		t.Errorf("EmptyIfNil(%v) = %v, want unchanged", in, got)
	}
}

// TestStringArray_ScanBothEncodings proves the StringArray scanner reads
// both the postgres text format `{a,b}` (the live write path) and legacy
// JSON `["a","b"]`, including embedded commas in quoted elements.
func TestStringArray_ScanBothEncodings(t *testing.T) {
	cases := map[string][]string{
		`{go,sql}`:               {"go", "sql"},
		`{"comma,inside",plain}`: {"comma,inside", "plain"},
		`["json","array"]`:       {"json", "array"},
		`{}`:                     nil,
	}
	for src, want := range cases {
		var out StringArray
		if err := out.Scan(src); err != nil {
			t.Fatalf("Scan(%q): %v", src, err)
		}
		if len(out) != len(want) {
			t.Fatalf("Scan(%q) len = %d, want %d (%v)", src, len(out), len(want), out)
		}
		for i := range want {
			if out[i] != want[i] {
				t.Errorf("Scan(%q)[%d] = %q, want %q", src, i, out[i], want[i])
			}
		}
	}
}

// TestInt64Array_ScanBothEncodings proves the Int64Array scanner reads
// both postgres text and legacy JSON integer arrays.
func TestInt64Array_ScanBothEncodings(t *testing.T) {
	cases := map[string][]int64{
		`{1,2,3}`: {1, 2, 3},
		`[4,5]`:   {4, 5},
		`{}`:      nil,
	}
	for src, want := range cases {
		var out Int64Array
		if err := out.Scan(src); err != nil {
			t.Fatalf("Scan(%q): %v", src, err)
		}
		if len(out) != len(want) {
			t.Fatalf("Scan(%q) len = %d, want %d (%v)", src, len(out), len(want), out)
		}
		for i := range want {
			if out[i] != want[i] {
				t.Errorf("Scan(%q)[%d] = %d, want %d", src, i, out[i], want[i])
			}
		}
	}
}
