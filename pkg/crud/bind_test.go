package crud

import (
	"bytes"
	"testing"
)

func TestBindArg(t *testing.T) {
	cases := []struct {
		lit  string
		want any
		ok   bool
	}{
		{"NULL", nil, true},
		{"TRUE", "TRUE", true},
		{"FALSE", "FALSE", true},
		{"42", "42", true},
		{"-7", "-7", true},
		{"0.30000000000000004", "0.30000000000000004", true},
		{"1e-07", "1e-07", true},
		{"''", "", true},
		{"'it''s'", "it's", true},
		{"'{\"a,b\",\"c''d\"}'", "{\"a,b\",\"c'd\"}", true},
		{"'2026-10-19 12:00:00+00:00'", "2026-10-19 12:00:00+00:00", true},
		// Not plain literals: inlined, never bound.
		{"DEFAULT", nil, false},
		{"'unterminated", nil, false},
		{"'a'b'", nil, false},
		{"now()", nil, false},
		{"?!(bun: error)", nil, false},
		{"1.", nil, false},
		{"-", nil, false},
		{"", nil, false},
	}
	for _, c := range cases {
		got, ok := bindArg([]byte(c.lit))
		if ok != c.ok || got != c.want {
			t.Errorf("bindArg(%q) = (%#v, %v), want (%#v, %v)", c.lit, got, ok, c.want, c.ok)
		}
	}
}

func TestByteaArg(t *testing.T) {
	if got, ok := byteaArg([]byte(`'\x00ff5c27'`)); !ok || !bytes.Equal(got, []byte{0x00, 0xff, 0x5c, 0x27}) {
		t.Errorf("hex literal = (%v, %v)", got, ok)
	}
	if got, ok := byteaArg([]byte(`'\x'`)); !ok || len(got) != 0 {
		t.Errorf("empty bytea = (%v, %v), want empty", got, ok)
	}
	for _, lit := range []string{`'abc'`, `'\xzz'`, `NULL`, `'\x0'`} {
		if _, ok := byteaArg([]byte(lit)); ok {
			t.Errorf("byteaArg(%q) decoded", lit)
		}
	}
}
