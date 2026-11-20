// Copyright (c) 2025 Reliant Labs
package github

import "strings"

// Markers: the machine-readable HTML comments a pipeline leaves in a review or
// comment body, `<!-- review-passed sha=<sha> -->`. A pipeline writes them at
// the END of the body, so a long one loses them to capText; the `markers` field
// kind (projection specs) reads them from the whole body first.
//
// A marker is, all on one line:
//
//	<!--  name  key=value ...  -->
//
// name is [a-z][a-z0-9:_-]{0,63}, key [a-z][a-z0-9_-]{0,31}, value 1-128
// bytes with no whitespace, control character or `>`, and the whole marker at
// most maxMarkerBytes. A comment holding anything else (free text, a quoted
// value, a repeated key) is not a marker. A marker in a fenced code block or an
// inline code span is quoted, not left, so it is not one either.
//
// The body is untrusted: the scan is one pass, and each candidate reads at most
// maxMarkerBytes.

const (
	// maxMarkers bounds the markers kept from one body, in body order.
	maxMarkers = 16
	// maxMarkerBytes bounds one marker, `<!--` through `-->`.
	maxMarkerBytes = 256

	maxMarkerName  = 64
	maxMarkerKey   = 32
	maxMarkerValue = 128
)

// markers is the markers of body, at most maxMarkers, each
// {"name": string, "attrs": {key: value}}; nil when there are none.
func markers(body string) []any {
	var out []any
	var fence string // the open code fence's marker (```, ~~~~), "" outside one
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if f := codeFence(line); f != "" {
			switch {
			case fence == "":
				fence = f
			case f[0] == fence[0] && len(f) >= len(fence) && strings.TrimSpace(strings.TrimLeft(line, " \t")[len(f):]) == "":
				fence = ""
			}
			continue
		}
		if fence != "" || !strings.Contains(line, "<!--") {
			continue
		}
		out = lineMarkers(line, out)
		if len(out) == maxMarkers {
			break
		}
	}
	return out
}

// codeFence is the run of ` or ~ (three or more) a fence line starts with,
// after indentation; "" when line is not one.
func codeFence(line string) string {
	s := strings.TrimLeft(line, " \t")
	if len(s) < 3 || (s[0] != '`' && s[0] != '~') {
		return ""
	}
	n := 0
	for n < len(s) && s[n] == s[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return s[:n]
}

// lineMarkers appends the markers of one line to out, outside inline code
// spans (a run of backticks opens one; the next run of the same length closes
// it; an unclosed span hides the rest of the line).
func lineMarkers(line string, out []any) []any {
	span := 0 // the open code span's backtick count; 0 outside one
	for i := 0; i < len(line) && len(out) < maxMarkers; {
		if line[i] == '`' {
			n := 0
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			switch span {
			case 0:
				span = n
			case n:
				span = 0
			}
			i += n
			continue
		}
		if span == 0 && strings.HasPrefix(line[i:], "<!--") {
			if m, end := parseMarker(line[i:]); m != nil {
				out = append(out, m)
				i += end
				continue
			}
			i += len("<!--")
			continue
		}
		i++
	}
	return out
}

// parseMarker reads the marker s starts with (s starts with "<!--"): the
// marker and its length, or nil when s does not start with one.
func parseMarker(s string) (map[string]any, int) {
	if len(s) > maxMarkerBytes {
		s = s[:maxMarkerBytes]
	}
	i := skipBlanks(s, len("<!--"))
	scan := func(from int, ok func(byte) bool) (string, int) {
		j := from
		for j < len(s) && ok(s[j]) {
			j++
		}
		return s[from:j], j
	}
	// A name or a value may run straight into the closing "-->" ("x=1-->"):
	// a token that ends in "--" followed by ">" is the token plus the close.
	token := func(from int, ok func(byte) bool) (string, int, bool) {
		tok, j := scan(from, ok)
		if j < len(s) && s[j] == '>' && strings.HasSuffix(tok, "--") {
			return tok[:len(tok)-2], j + 1, true
		}
		return tok, j, false
	}

	name, i, closed := token(i, isNameByte)
	if !validIdent(name, maxMarkerName) {
		return nil, 0
	}
	attrs := map[string]any{}
	for !closed {
		next := skipBlanks(s, i)
		if strings.HasPrefix(s[next:], "-->") {
			i = next + len("-->")
			break
		}
		if next == i { // a token must be followed by a blank or the close
			return nil, 0
		}
		i = next
		var key, value string
		key, i = scan(i, isKeyByte)
		if !validIdent(key, maxMarkerKey) || i >= len(s) || s[i] != '=' {
			return nil, 0
		}
		value, i, closed = token(i+1, isValueByte)
		if value == "" || len(value) > maxMarkerValue {
			return nil, 0
		}
		if _, dup := attrs[key]; dup {
			return nil, 0
		}
		attrs[key] = value
	}
	return map[string]any{"name": name, "attrs": attrs}, i
}

func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// validIdent: [a-z] then at most max-1 more bytes (the byte class was
// checked by the token's scan).
func validIdent(s string, max int) bool {
	return len(s) >= 1 && len(s) <= max && s[0] >= 'a' && s[0] <= 'z'
}

func isLowerDigit(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }

func isNameByte(c byte) bool { return isLowerDigit(c) || c == ':' || c == '_' || c == '-' }

func isKeyByte(c byte) bool { return isLowerDigit(c) || c == '_' || c == '-' }

// isValueByte: not whitespace, not a control character, not '>'.
func isValueByte(c byte) bool { return c > ' ' && c != 0x7f && c != '>' }
