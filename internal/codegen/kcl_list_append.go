package codegen

import (
	"regexp"
	"strings"
)

// kclListScan is what appendKCLListElement needs to know about one KCL list
// literal, read with KCL's own lexical rules: `#` starts a comment that runs
// to the end of the line, strings may hold any bracket, and nested
// brackets/braces/parens belong to an element rather than to the list.
//
// A regexp cannot answer these questions. The splice it replaced captured
// `[^\]]*` — which a `]` inside a comment ends early — then trimmed and
// re-joined the body with ", ", which reflowed a multi-line list onto one line
// and turned a trailing comma into `,,`.
type kclListScan struct {
	// close is the index of the list's own closing `]`.
	close int
	// lastCode is the index of the last byte inside the list that is neither
	// whitespace nor comment, or -1 when the list has no entries.
	lastCode int
	// commas reports a comma at the list's own depth — whether this list
	// separates its entries with commas rather than newlines alone.
	commas bool
	// code is the list body (between the brackets) with every comment
	// blanked to spaces, so a search for an entry cannot match prose.
	code string
}

// scanKCLList scans the list literal whose `[` is at src[open]. ok is false
// when the list is never closed — the file is not KCL this code can edit.
func scanKCLList(src string, open int) (kclListScan, bool) {
	s := kclListScan{lastCode: -1}
	code := []byte(src[open+1:])
	blank := func(from, to int) {
		for j := from; j < to; j++ {
			if code[j-open-1] != '\n' {
				code[j-open-1] = ' '
			}
		}
	}
	depth := 0
	for i := open + 1; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '#':
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = len(src) - i
			}
			blank(i, i+end)
			i += end - 1
			continue
		case c == '"' || c == '\'':
			end := kclStringEnd(src, i)
			if end < 0 {
				return s, false
			}
			s.lastCode = end - 1
			i = end - 1
			continue
		case c == '[' || c == '{' || c == '(':
			depth++
		case c == ']' && depth == 0:
			s.close = i
			s.code = string(code[:i-open-1])
			return s, true
		case c == ']' || c == '}' || c == ')':
			depth--
		case c == ',' && depth == 0:
			s.commas = true
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			s.lastCode = i
		}
	}
	return s, false
}

// kclStringEnd returns the index just past the string literal opening at
// src[start], or -1 when it is unterminated. It handles both quote styles,
// triple-quoted strings, and backslash escapes.
func kclStringEnd(src string, start int) int {
	q := src[start : start+1]
	if strings.HasPrefix(src[start:], q+q+q) {
		end := strings.Index(src[start+3:], q+q+q)
		if end < 0 {
			return -1
		}
		return start + 3 + end + 3
	}
	for i := start + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case q[0]:
			return i + 1
		case '\n':
			return -1
		}
	}
	return -1
}

// kclListHasEntry reports whether a scanned list already names ident as an
// entry. Comments are blanked first, so a commented-out entry does not count.
func kclListHasEntry(s kclListScan, ident string) bool {
	return regexp.MustCompile(`(^|[\s,\[])` + regexp.QuoteMeta(ident) + `(\s|,|$)`).MatchString(s.code)
}

// appendKCLListElement adds elem as the LAST entry of the list literal whose
// `[` is at src[open], in the style the list is already written in, and
// returns the edited source. It edits nothing else: no other entry, comment
// or line moves, so the diff is the one new entry.
//
// KCL lists are indentation-sensitive, and the placement follows from that:
//
//   - Last entry on the `[` line (`[a, b]`, `[a, b,\n]`), or on the `]` line:
//     the new entry joins that line. An indented line below `[a, b,` is a
//     syntax error.
//   - Last entry on a line of its own: the new entry gets the next line, at
//     the same indentation — after any trailing comment on the last entry,
//     before any commented-out entries that follow it.
//   - Separators match the list's: a trailing comma is kept trailing; a
//     comma-separated list whose last entry lacks one gets one; a list that
//     separates entries by newline alone stays that way.
//
// ok is false, with src returned unchanged, when the list is unterminated.
func appendKCLListElement(src string, open int, elem string) (string, bool) {
	s, ok := scanKCLList(src, open)
	if !ok {
		return src, false
	}

	if s.lastCode < 0 {
		if !strings.Contains(src[open+1:s.close], "\n") {
			return src[:open+1] + elem + src[s.close:], true
		}
		// An empty multi-line list, holding at most comments: the entry goes
		// on its own line just above the `]`, one level in from it.
		at := lineStart(src, s.close)
		return src[:at] + indentAt(src, s.close) + "    " + elem + ",\n" + src[at:], true
	}

	lastIsComma := src[s.lastCode] == ','
	lastLine := lineStart(src, s.lastCode)
	if lastLine == lineStart(src, open) || lastLine == lineStart(src, s.close) {
		at := s.lastCode + 1
		if lastIsComma {
			return src[:at] + " " + elem + "," + src[at:], true
		}
		return src[:at] + ", " + elem + src[at:], true
	}

	// The last entry ends a line of its own, and the `]` is on a later one,
	// so that line has a newline: the new entry starts right after it.
	next := s.lastCode + strings.IndexByte(src[s.lastCode:], '\n') + 1
	indent := indentAt(src, s.lastCode)
	switch {
	case lastIsComma:
		return src[:next] + indent + elem + ",\n" + src[next:], true
	case s.commas:
		return src[:s.lastCode+1] + "," + src[s.lastCode+1:next] + indent + elem + "\n" + src[next:], true
	default:
		return src[:next] + indent + elem + "\n" + src[next:], true
	}
}

// lineStart returns the index of the first byte of the line holding src[i].
func lineStart(src string, i int) int {
	return strings.LastIndexByte(src[:i], '\n') + 1
}

// indentAt returns the leading whitespace of the line holding src[i].
func indentAt(src string, i int) string {
	start := lineStart(src, i)
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return src[start:end]
}
