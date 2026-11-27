package obsout

import (
	"io"
	"unicode/utf8"
)

// Telemetry text (a URL, a selector, an error message, a span attribute) is
// written by whoever holds the public ingest key, so it reaches a terminal as
// untrusted bytes. [NewSanitizingWriter] is the one place the human form of a
// `forge obs` verb is made safe to print: it removes ANSI/OSC/DCS escape
// sequences, C0 and C1 control characters other than newline and tab, DEL and
// the bidirectional-override characters. JSON is unaffected in practice (the
// encoder already escapes control characters).

const (
	stNormal = iota
	stEsc    // after ESC
	stCSI    // inside ESC [ ... until a final byte
	stString // inside OSC / DCS / SOS / PM / APC until BEL or ST
	stStrEsc // ESC seen inside a string: ESC \ is the terminator
)

type sanitizer struct {
	w       io.Writer
	state   int
	pending []byte // an incomplete UTF-8 sequence carried between writes
}

// NewSanitizingWriter returns a writer that passes printable text through to w
// and drops everything that can steer a terminal. An escape sequence never
// outlives the line it started on, so an unterminated one cannot swallow later
// output.
func NewSanitizingWriter(w io.Writer) io.Writer { return &sanitizer{w: w} }

// SanitizeText is [NewSanitizingWriter] over one string, for a renderer that
// builds a cell before it writes.
func SanitizeText(s string) string {
	var b sinkBuffer
	sw := &sanitizer{w: &b}
	_, _ = sw.Write([]byte(s))
	sw.flushPending()
	return string(b)
}

type sinkBuffer []byte

func (b *sinkBuffer) Write(p []byte) (int, error) { *b = append(*b, p...); return len(p), nil }

func (s *sanitizer) Write(p []byte) (int, error) {
	out := make([]byte, 0, len(p))
	for _, c := range p {
		out = s.step(out, c)
	}
	if len(out) > 0 {
		if _, err := s.w.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// flushPending drops an incomplete trailing UTF-8 sequence as U+FFFD.
func (s *sanitizer) flushPending() {
	if len(s.pending) > 0 {
		s.pending = nil
		_, _ = s.w.Write([]byte("\uFFFD"))
	}
}

func (s *sanitizer) step(out []byte, c byte) []byte {
	if len(s.pending) > 0 {
		if c&0xC0 != 0x80 { // the sequence was cut short
			s.pending = nil
			out = append(out, "\uFFFD"...)
			return s.step(out, c)
		}
		s.pending = append(s.pending, c)
		if !utf8.FullRune(s.pending) {
			return out
		}
		r, _ := utf8.DecodeRune(s.pending)
		s.pending = s.pending[:0]
		return s.rune(out, r)
	}
	if c >= 0x80 {
		if c < 0xC2 || c > 0xF4 {
			return append(out, "\uFFFD"...)
		}
		s.pending = append(s.pending, c)
		return out
	}
	return s.ascii(out, c)
}

func (s *sanitizer) ascii(out []byte, c byte) []byte {
	switch s.state {
	case stEsc:
		switch {
		case c == '[':
			s.state = stCSI
		case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
			s.state = stString
		case c == 0x1b:
		case c >= 0x20 && c <= 0x2f: // intermediate bytes of a longer escape
		case c == '\n':
			s.state = stNormal
			out = append(out, '\n')
		default:
			s.state = stNormal
		}
		return out
	case stCSI:
		switch {
		case c == 0x1b:
			s.state = stEsc
		case c == '\n':
			s.state = stNormal
			out = append(out, '\n')
		case c >= 0x40 && c <= 0x7e:
			s.state = stNormal
		}
		return out
	case stString:
		switch c {
		case 0x07:
			s.state = stNormal
		case 0x1b:
			s.state = stStrEsc
		case '\n':
			s.state = stNormal
			out = append(out, '\n')
		}
		return out
	case stStrEsc:
		switch c {
		case '\\':
			s.state = stNormal
		case '\n':
			s.state = stNormal
			out = append(out, '\n')
		case 0x1b:
		default:
			s.state = stString
		}
		return out
	}
	switch {
	case c == 0x1b:
		s.state = stEsc
	case c == '\n' || c == '\t':
		out = append(out, c)
	case c < 0x20 || c == 0x7f:
	default:
		out = append(out, c)
	}
	return out
}

// rune handles a decoded multi-byte rune in the normal state; inside an escape
// sequence its bytes are swallowed with the rest of it.
func (s *sanitizer) rune(out []byte, r rune) []byte {
	if s.state != stNormal {
		if r >= 0x80 && r <= 0x9f && s.state == stString && r == 0x9c {
			s.state = stNormal // C1 ST
		}
		return out
	}
	switch {
	case r == utf8.RuneError:
		return append(out, "\uFFFD"...)
	case r == 0x9b: // C1 CSI
		s.state = stCSI
		return out
	case r == 0x90 || r == 0x98 || r == 0x9d || r == 0x9e || r == 0x9f: // C1 DCS SOS OSC PM APC
		s.state = stString
		return out
	case r >= 0x80 && r <= 0x9f:
		return out
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
		return out
	}
	return utf8.AppendRune(out, r)
}
