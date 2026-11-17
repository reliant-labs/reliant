package obsout

import (
	"bytes"
	"strings"
	"testing"
)

func TestSanitizeText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "GET /jobs → 500\tok\nnext", "GET /jobs → 500\tok\nnext"},
		{"sgr", "\x1b[31mred\x1b[0m", "red"},
		{"cursor and clear", "a\x1b[2J\x1b[1;1Hb", "ab"},
		{"osc8 hyperlink bel", "\x1b]8;;https://evil.example\x07click\x1b]8;;\x07", "click"},
		{"osc8 hyperlink st", "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"osc title", "x\x1b]0;pwned\x07y", "xy"},
		{"dcs", "x\x1bPq#0;2;0;0;0\x1b\\y", "xy"},
		{"c1 csi", "x\u009b31my", "xy"},
		{"c1 osc", "x\u009d0;t\u009cy", "xy"},
		{"bare c0", "a\x00b\x07c\x08d\re\x7ff", "abcdef"},
		{"bidi override", "evil\u202egpj.exe", "evilgpj.exe"},
		{"unterminated osc dies at newline", "a\x1b]0;never ends\nline two", "a\nline two"},
		{"unterminated csi dies at newline", "a\x1b[31\nline two", "a\nline two"},
		{"esc then letter", "a\x1bcb", "ab"},
		{"invalid utf8", "a\xffb", "a\uFFFDb"},
	}
	for _, c := range cases {
		if got := SanitizeText(c.in); got != c.want {
			t.Errorf("%s: SanitizeText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// An escape sequence split across writes, or a multi-byte rune split across
// them, is still caught: output arrives in whatever chunks fmt produced.
func TestSanitizingWriterIsStatefulAcrossWrites(t *testing.T) {
	in := "\x1b]8;;https://evil.example\x07click\x1b]8;;\x07 → \x1b[3" + "1mred"
	for cut := 1; cut < len(in); cut++ {
		var b bytes.Buffer
		w := NewSanitizingWriter(&b)
		if _, err := w.Write([]byte(in[:cut])); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(in[cut:])); err != nil {
			t.Fatal(err)
		}
		if got, want := b.String(), "click → red"; got != want {
			t.Fatalf("cut at %d: got %q, want %q", cut, got, want)
		}
	}
	if strings.ContainsRune(SanitizeText(in), 0x1b) {
		t.Error("an ESC survived")
	}
}
