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
// value, a repeated key) is not a marker.
//
// A marker is one only where GitHub renders it as an HTML comment, not as code:
// a quoted marker must not count. So the body is read the way GitHub's
// Markdown (GFM) reads it, as far as code is concerned:
//
//   - a fenced code block (``` or ~~~) hides its lines;
//   - an indented code block (4 columns, not continuing a paragraph) hides its lines;
//   - a line that starts with `<!--` (up to 3 spaces in) opens an HTML block,
//     which interrupts a paragraph and ends at the line holding `-->`: its
//     backticks are text, not code;
//   - a paragraph (lines up to a blank line, a fence or an HTML block) is read
//     left to right, once, as CommonMark's inline parser reads it. A backtick
//     run of N the walk reaches opens a code span when a later run of exactly N
//     in the paragraph closes it (newlines included); the span hides all it
//     holds, other backtick runs included, and the walk resumes after its
//     closer. A run with no closer is a literal backtick. A `<!--` the walk
//     reaches opens an HTML comment through the first `-->` (`<!-->` and
//     `<!--->` are whole comments), across lines: its backticks are text.
//
// A marker is a whole HTML comment that matches the grammar; a comment that
// does not (free text) hides any marker-like text inside it, as it does in
// GitHub's rendering.
//
// Not modelled (each errs toward fewer markers): backslash-escaped backticks
// and list and quote containers.
//
// The body is untrusted: every pass is linear in it, and each candidate marker
// reads at most maxMarkerBytes.

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
	var s markerScan
	s.scan(body)
	return s.out
}

// markerScan is one scan of a body.
type markerScan struct {
	out []any
	// steps counts the bytes the scan visits (a fixed number of passes over
	// each line count once), so a test can prove the scan linear without a
	// clock.
	steps int
}

func (s *markerScan) full() bool { return len(s.out) >= maxMarkers }

func (s *markerScan) scan(body string) {
	fence := ""            // the open code fence's run (```, ~~~~); "" outside one
	html := -1             // the open HTML block's start in body; -1 when none
	para, paraEnd := -1, 0 // the open paragraph's byte range in body; para -1 when none
	flush := func() {
		if para >= 0 {
			s.inline(body[para:paraEnd], true)
			para = -1
		}
	}
	// endHTML reads the HTML block that started at html once its line holding
	// `-->` (ending at end) is reached, or the body ends.
	endHTML := func(end int) {
		s.inline(body[html:end], false)
		html = -1
	}
	pos := 0
	for raw := range strings.SplitSeq(body, "\n") {
		if s.full() {
			return
		}
		start := pos
		pos += len(raw) + 1
		s.steps += len(raw) + 1
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case fence != "":
			if closesFence(line, fence) {
				fence = ""
			}
		case html >= 0:
			if strings.Contains(line, "-->") {
				endHTML(start + len(raw))
			}
		case codeFence(line) != "":
			flush()
			fence = codeFence(line)
		case strings.TrimSpace(line) == "":
			flush()
		case para < 0 && indentedCode(line):
		case htmlCommentStart(line):
			flush()
			html = start
			if strings.Contains(line, "-->") {
				endHTML(start + len(raw))
			}
		default:
			if para < 0 {
				para = start
			}
			paraEnd = start + len(raw)
		}
	}
	flush()
	if html >= 0 {
		endHTML(len(body))
	}
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

// closesFence: line is a fence of fence's character, at least as long, with
// nothing after it.
func closesFence(line, fence string) bool {
	f := codeFence(line)
	return f != "" && f[0] == fence[0] && len(f) >= len(fence) &&
		strings.TrimSpace(strings.TrimLeft(line, " \t")[len(f):]) == ""
}

// indentedCode: line is indented 4 columns or more (a tab is 4).
func indentedCode(line string) bool {
	col := 0
	for i := 0; i < len(line) && col < 4; i++ {
		switch line[i] {
		case ' ':
			col++
		case '\t':
			col = 4
		default:
			return false
		}
	}
	return col >= 4
}

// htmlCommentStart: line opens an HTML comment block, `<!--` after at most 3
// spaces.
func htmlCommentStart(line string) bool {
	i := 0
	for i < 3 && i < len(line) && line[i] == ' ' {
		i++
	}
	return strings.HasPrefix(line[i:], "<!--")
}

// backtickRun is one run of backticks in a paragraph, and next, the index of
// the nearest later run of the same length (-1: none). next is where a code
// span the run OPENS would close; the walk consults it only for a run it
// reaches outside any span or comment, so a run inside a span is text.
type backtickRun struct{ pos, n, next int }

// backtickRuns is text's runs of backticks, with their next.
func backtickRuns(text string) []backtickRun {
	var runs []backtickRun
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(text) && text[j] == '`' {
			j++
		}
		runs = append(runs, backtickRun{pos: i, n: j - i, next: -1})
		i = j
	}
	later := map[int]int{} // run length -> index of the nearest later run of it
	for k := len(runs) - 1; k >= 0; k-- {
		if c, ok := later[runs[k].n]; ok {
			runs[k].next = c
		}
		later[runs[k].n] = k
	}
	return runs
}

// inline appends the markers of text, a paragraph (spans: backtick runs are
// code-span delimiters) or an HTML block (spans false: backticks are text).
// It is one left-to-right walk: a code span or an HTML comment it opens is
// skipped whole, and the walk resumes after it.
func (s *markerScan) inline(text string, spans bool) {
	if !strings.Contains(text, "<!--") {
		s.steps += len(text)
		return
	}
	var runs []backtickRun
	if spans {
		runs = backtickRuns(text)
		s.steps += len(text)
	}
	unclosed := false // no `-->` follows: every later `<!--` is literal text
	r := 0            // the first run at or after i
	for i := 0; i < len(text) && !s.full(); {
		s.steps++
		for r < len(runs) && runs[r].pos < i { // runs inside a span or comment just skipped
			r++
		}
		if r < len(runs) && runs[r].pos == i {
			if c := runs[r].next; c >= 0 { // opens a code span: resume after its closer
				i, r = runs[c].pos+runs[c].n, c+1
			} else { // no closer: a literal backtick
				i, r = i+runs[r].n, r+1
			}
			continue
		}
		if !strings.HasPrefix(text[i:], "<!--") {
			i++
			continue
		}
		end := s.commentEnd(text, i, &unclosed)
		if end < 0 { // never closed: literal text
			i += len("<!--")
			continue
		}
		if m, n := parseMarker(text[i:]); m != nil && i+n == end {
			s.out = append(s.out, m)
		}
		i = end
	}
}

// commentEnd is the end of the HTML comment that starts at text[i:] ("<!--"),
// or -1 when no `-->` closes it (and then none closes a later one: *unclosed
// is set so the search is not repeated).
func (s *markerScan) commentEnd(text string, i int, unclosed *bool) int {
	switch {
	case strings.HasPrefix(text[i:], "<!-->"):
		return i + len("<!-->")
	case strings.HasPrefix(text[i:], "<!--->"):
		return i + len("<!--->")
	case *unclosed:
		return -1
	}
	from := i + len("<!--")
	j := strings.Index(text[from:], "-->")
	if j < 0 {
		s.steps += len(text) - from
		*unclosed = true
		return -1
	}
	s.steps += j + len("-->")
	return from + j + len("-->")
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
