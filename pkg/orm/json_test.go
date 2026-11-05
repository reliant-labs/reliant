package orm

import (
	"reflect"
	"testing"
)

func TestJSONValueAndScanRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   any
		dst  func() (ptr any, get func() any)
	}{
		{
			name: "string slice",
			in:   []string{"go", "bookmarks", "comma,inside"},
			dst: func() (any, func() any) {
				var v []string
				return &v, func() any { return v }
			},
		},
		{
			name: "int64 slice",
			in:   []int64{1, -2, 9000000000},
			dst: func() (any, func() any) {
				var v []int64
				return &v, func() any { return v }
			},
		},
		{
			name: "bool slice",
			in:   []bool{true, false, true},
			dst: func() (any, func() any) {
				var v []bool
				return &v, func() any { return v }
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, err := JSON(tc.in).Value()
			if err != nil {
				t.Fatalf("Value() error = %v", err)
			}
			ptr, get := tc.dst()
			if err := ScanJSON(ptr).Scan(val); err != nil {
				t.Fatalf("Scan() error = %v", err)
			}
			if !reflect.DeepEqual(get(), tc.in) {
				t.Errorf("round trip = %#v, want %#v", get(), tc.in)
			}
		})
	}
}

func TestJSONNilSliceRoundTripsAsNULL(t *testing.T) {
	var in []string
	val, err := JSON(in).Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	if val != nil {
		t.Errorf("nil slice should store SQL NULL, got %#v", val)
	}
	var out []string
	if err := ScanJSON(&out).Scan(nil); err != nil {
		t.Fatalf("Scan(nil) error = %v", err)
	}
	if out != nil {
		t.Errorf("NULL should scan to nil slice, got %#v", out)
	}
}

func TestJSONScanAcceptsBytesAndString(t *testing.T) {
	for _, src := range []any{[]byte(`["a","b"]`), `["a","b"]`} {
		var out []string
		if err := ScanJSON(&out).Scan(src); err != nil {
			t.Fatalf("Scan(%T) error = %v", src, err)
		}
		if !reflect.DeepEqual(out, []string{"a", "b"}) {
			t.Errorf("Scan(%T) = %#v", src, out)
		}
	}
}
