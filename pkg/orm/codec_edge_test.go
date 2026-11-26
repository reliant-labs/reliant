package orm

import (
	"reflect"
	"strings"
	"testing"
)

func TestArrayScan_Errors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scan    func() error
		wantErr string
	}{
		{"StringArray from an int", func() error { var a StringArray; return a.Scan(42) }, "cannot scan int into StringArray"},
		{"Int64Array from a float", func() error { var a Int64Array; return a.Scan(1.5) }, "cannot scan float64 into Int64Array"},
		{"StringArray, unrecognized encoding", func() error { var a StringArray; return a.Scan("plain") }, `unrecognized array encoding "plain"`},
		{"Int64Array, unrecognized encoding", func() error { var a Int64Array; return a.Scan("plain") }, `unrecognized array encoding "plain"`},
		{"StringArray, bad JSON", func() error { var a StringArray; return a.Scan(`["a"`) }, "unexpected end of JSON input"},
		{"StringArray, brace without close", func() error { var a StringArray; return a.Scan(`{a,b`) }, `malformed postgres array "{a,b"`},
		{"Int64Array, brace without close", func() error { var a Int64Array; return a.Scan(`{1,2`) }, `malformed postgres array "{1,2"`},
		{"Int64Array, non-integer element", func() error { var a Int64Array; return a.Scan(`{1,x}`) }, `array element "x" is not an integer`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.scan(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestArrayScan_NullAndBytesSources(t *testing.T) {
	for _, src := range []any{nil, "", "null", []byte("null"), []byte{}} {
		a := StringArray{"stale"}
		if err := a.Scan(src); err != nil || a != nil {
			t.Errorf("StringArray.Scan(%#v) = %v, %#v; want nil, nil", src, err, a)
		}
		n := Int64Array{9}
		if err := n.Scan(src); err != nil || n != nil {
			t.Errorf("Int64Array.Scan(%#v) = %v, %#v; want nil, nil", src, err, n)
		}
	}
	var a StringArray
	if err := a.Scan([]byte(`{x,"y z"}`)); err != nil || !reflect.DeepEqual([]string(a), []string{"x", "y z"}) {
		t.Errorf("a []byte source: %v, %#v", err, a)
	}
}

func TestParsePGTextArray(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`{}`, nil},
		{`{a}`, []string{"a"}},
		{`{a,b,c}`, []string{"a", "b", "c"}},
		{`{"a b","c,d"}`, []string{"a b", "c,d"}},
		{`{"quote\"inside","back\\slash"}`, []string{`quote"inside`, `back\slash`}},
		{`{NULL,a}`, []string{"", "a"}},
		{`{"",a}`, []string{"", "a"}},
	} {
		got, err := parsePGTextArray(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parsePGTextArray(%s) = %#v, %v; want %#v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{``, `a,b`, `{a`, `a}`} {
		if _, err := parsePGTextArray(bad); err == nil {
			t.Errorf("parsePGTextArray(%q) accepted a malformed array", bad)
		}
	}
}

func TestJSONValue_NilAndErrors(t *testing.T) {
	if v, err := JSON(nil).Value(); v != nil || err != nil {
		t.Errorf("JSON(nil).Value() = %v, %v; want SQL NULL", v, err)
	}
	if v, err := JSON([]string(nil)).Value(); v != nil || err != nil {
		t.Errorf("JSON(nil slice).Value() = %v, %v; want SQL NULL", v, err)
	}
	if v, err := JSON(map[string]int{"a": 1}).Value(); v != `{"a":1}` || err != nil {
		t.Errorf("JSON(map).Value() = %v, %v", v, err)
	}
	if _, err := JSON(make(chan int)).Value(); err == nil || !strings.Contains(err.Error(), "marshal JSON column value") {
		t.Errorf("an unmarshalable value: err = %v", err)
	}
}

func TestScanJSON_Errors(t *testing.T) {
	var out []string
	if err := ScanJSON(&out).Scan(12); err == nil || !strings.Contains(err.Error(), "cannot scan int into JSON column destination *[]string") {
		t.Errorf("a non-text source: err = %v", err)
	}
	if err := ScanJSON(&out).Scan(`{"not":"a list"}`); err == nil || !strings.Contains(err.Error(), "unmarshal JSON column into *[]string") {
		t.Errorf("a document of the wrong shape: err = %v", err)
	}
	out = []string{"keep"}
	for _, absent := range []any{nil, []byte{}, "", "null"} {
		if err := ScanJSON(&out).Scan(absent); err != nil || !reflect.DeepEqual(out, []string{"keep"}) {
			t.Errorf("Scan(%#v) = %v, dst %v; want dst untouched", absent, err, out)
		}
	}
}

func TestJSONBMap_RoundTripAndErrors(t *testing.T) {
	s, err := MarshalJSONBMap(map[string]string{"k": "v"})
	if err != nil || s != `{"k":"v"}` {
		t.Fatalf("MarshalJSONBMap = %q, %v", s, err)
	}
	var out map[string]string
	if err := UnmarshalJSONBMap(s, &out); err != nil || !reflect.DeepEqual(out, map[string]string{"k": "v"}) {
		t.Fatalf("UnmarshalJSONBMap = %v, %v", out, err)
	}
	// A nil or empty map stores the object shape, never null.
	for _, m := range []any{map[string]string(nil), map[string]string{}} {
		if s, err := MarshalJSONBMap(m); err != nil || s != "{}" {
			t.Errorf("MarshalJSONBMap(%#v) = %q, %v; want {}", m, s, err)
		}
	}
	if _, err := MarshalJSONBMap(map[string]any{"c": make(chan int)}); err == nil || !strings.Contains(err.Error(), "into a jsonb column") {
		t.Errorf("an unmarshalable map: err = %v", err)
	}
	if _, err := MarshalJSONBScalars(make(chan int)); err == nil || !strings.Contains(err.Error(), "into a jsonb column") {
		t.Errorf("unmarshalable scalars: err = %v", err)
	}
	for _, absent := range []string{"", "null"} {
		out := map[string]string{"keep": "me"}
		if err := UnmarshalJSONBMap(absent, &out); err != nil || len(out) != 1 {
			t.Errorf("UnmarshalJSONBMap(%q) = %v, %v; want dst untouched", absent, out, err)
		}
	}
	if err := UnmarshalJSONBMap(`[1]`, &out); err == nil || !strings.Contains(err.Error(), "parse jsonb column into *map[string]string") {
		t.Errorf("an array where a map belongs: err = %v", err)
	}
	if err := UnmarshalJSONBScalars(`{`, &[]string{}); err == nil {
		t.Error("corrupt scalars document accepted")
	}
}

func TestInlineValues_MarksTheContext(t *testing.T) {
	ctx := InlineValues(t.Context())
	if ctx.Value(inlineKey{}) == nil {
		t.Fatal("InlineValues did not mark the ctx")
	}
	if t.Context().Value(inlineKey{}) != nil {
		t.Fatal("the parent ctx was marked")
	}
}

func TestLevelNameAndIsolationString(t *testing.T) {
	for level, want := range map[Isolation]string{
		ReadCommitted: "ReadCommitted", RepeatableRead: "RepeatableRead", Serializable: "Serializable",
	} {
		if got := levelName(level); got != want {
			t.Errorf("levelName(%v) = %q, want %q", level, got, want)
		}
	}
	for level, want := range map[Isolation]string{
		ReadCommitted: "read committed", RepeatableRead: "repeatable read", Serializable: "serializable",
		0: "unset", Isolation(99): "Isolation(99)",
	} {
		if got := level.String(); got != want {
			t.Errorf("Isolation(%d).String() = %q, want %q", int(level), got, want)
		}
	}
}
