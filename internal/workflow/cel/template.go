package wfcel

import (
	"fmt"
	"strings"
)

// A workflow template is text with {{ expr }} holes. Every place that reads one
// (runtime evaluation, validation, trigger inputs and prompts) goes through
// ScanTemplate, so "what is an expression" has one answer.
//
// The scanner reads the inside of {{ }} as CEL, not as bytes: a brace inside a
// string literal ('{', "}}", r'\') is data, a backslash escapes in an ordinary
// literal, and a newline is just whitespace. A {{ that never reaches its }} is
// an error with a position; it is never quietly treated as literal text, which
// would ship a prompt or a shell command containing the template source.
//
// To write a literal {{ in a template, make it an expression: {{ '{{' }}.

// TemplateMatch is one {{ expr }} hole in a template.
type TemplateMatch struct {
	// Full is the hole including its delimiters.
	Full string
	// Expr is the CEL inside, trimmed of surrounding whitespace.
	Expr string
	// Start and End are byte offsets of Full in the scanned string.
	Start, End int
	// ExprStart is the byte offset of the first byte of Expr (after trimming).
	ExprStart int
}

// TemplateSyntaxError reports a {{ that cannot be read as a template hole.
type TemplateSyntaxError struct {
	// Offset is the byte offset of the problem in the scanned string.
	Offset int
	// Line and Column are 1-based; Column counts bytes from the line start.
	Line, Column int
	Message      string
}

func (e *TemplateSyntaxError) Error() string {
	return fmt.Sprintf("template syntax error at line %d, column %d (offset %d): %s", e.Line, e.Column, e.Offset, e.Message)
}

func newTemplateSyntaxError(input string, offset int, format string, args ...any) *TemplateSyntaxError {
	line, col := 1, 1
	for i := 0; i < offset && i < len(input); i++ {
		if input[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return &TemplateSyntaxError{Offset: offset, Line: line, Column: col, Message: fmt.Sprintf(format, args...)}
}

// ContainsTemplate reports whether s has a {{ in it, balanced or not. A string
// that does is a template, and must scan cleanly.
func ContainsTemplate(s string) bool { return strings.Contains(s, "{{") }

// ScanTemplate returns the {{ expr }} holes of input in order. An input with no
// {{ has none. A {{ with no matching }}, or an unterminated string literal
// inside one, is a *TemplateSyntaxError.
func ScanTemplate(input string) ([]TemplateMatch, error) {
	var matches []TemplateMatch
	for i := 0; i < len(input); {
		open := strings.Index(input[i:], "{{")
		if open < 0 {
			break
		}
		start := i + open
		end, err := scanHole(input, start)
		if err != nil {
			return nil, err
		}
		inner := input[start+2 : end-2]
		lead := len(inner) - len(strings.TrimLeft(inner, " \t\r\n"))
		expr := strings.TrimSpace(inner)
		matches = append(matches, TemplateMatch{
			Full:      input[start:end],
			Expr:      expr,
			Start:     start,
			End:       end,
			ExprStart: start + 2 + lead,
		})
		i = end
	}
	return matches, nil
}

// scanHole returns the offset just past the }} that closes the {{ at start.
func scanHole(input string, start int) (int, error) {
	depth := 0 // open { and [ and ( do not matter; only map-literal braces can hide a }}
	for j := start + 2; j < len(input); {
		c := input[j]
		switch c {
		case '\'', '"', '`':
			next, err := skipQuoted(input, j, rawPrefixAt(input, j, start+2))
			if err != nil {
				return 0, err
			}
			j = next
			continue
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			} else if j+1 < len(input) && input[j+1] == '}' {
				return j + 2, nil
			}
		}
		j++
	}
	return 0, newTemplateSyntaxError(input, start,
		`"{{" has no closing "}}" (an unbalanced { or an unclosed string inside the expression hides it)`)
}

// rawPrefixAt reports whether the quote at j is the opening quote of a raw
// string literal (r'..', R"..", also br/rb): a preceding r or R that is not the
// tail of a longer identifier.
func rawPrefixAt(input string, j, floor int) bool {
	k := j - 1
	for k >= floor && (input[k] == 'b' || input[k] == 'B') {
		k--
	}
	if k < floor || (input[k] != 'r' && input[k] != 'R') {
		return false
	}
	if k > floor && isIdentByte(input[k-1]) {
		// `bar'x'` is not a raw literal; only a lone r/R (optionally with b) is.
		prev := input[k-1]
		if (prev != 'b' && prev != 'B') || (k-2 >= floor && isIdentByte(input[k-2])) {
			return false
		}
	}
	return true
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// skipQuoted returns the offset just past the quoted token opening at j. Triple
// quotes may span lines; single-quote forms may not, matching CEL. Backslash
// escapes the next byte unless the literal is raw. Backticks quote identifiers
// and have no escapes.
func skipQuoted(input string, j int, raw bool) (int, error) {
	q := input[j]
	if q == '`' {
		for k := j + 1; k < len(input); k++ {
			if input[k] == '`' {
				return k + 1, nil
			}
			if input[k] == '\n' {
				break
			}
		}
		return 0, newTemplateSyntaxError(input, j, "unterminated `quoted identifier` inside {{ }}")
	}
	triple := j+2 < len(input) && input[j+1] == q && input[j+2] == q
	k := j + 1
	if triple {
		k = j + 3
	}
	for k < len(input) {
		c := input[k]
		switch {
		case c == '\\' && !raw:
			k += 2
		case triple && c == q && strings.HasPrefix(input[k:], string([]byte{q, q, q})):
			return k + 3, nil
		case !triple && c == q:
			return k + 1, nil
		case !triple && c == '\n':
			return 0, newTemplateSyntaxError(input, j, "string literal inside {{ }} is not closed before the end of the line")
		default:
			k++
		}
	}
	return 0, newTemplateSyntaxError(input, j, "unterminated string literal inside {{ }}")
}

// RawCELTemplateDelimiter finds a {{ in an expression that is meant to be raw
// CEL (a condition, a while, a filter), ignoring any inside string literals,
// where it is data. Raw CEL fields are not templates; a {{ here is the mistake
// of wrapping them.
func RawCELTemplateDelimiter(expr string) (offset int, found bool) {
	for j := 0; j < len(expr); {
		c := expr[j]
		switch {
		case c == '\'' || c == '"' || c == '`':
			next, err := skipQuoted(expr, j, rawPrefixAt(expr, j, 0))
			if err != nil {
				return 0, false // CEL's own parser reports the open string
			}
			j = next
			continue
		case c == '{' && j+1 < len(expr) && expr[j+1] == '{':
			return j, true
		}
		j++
	}
	return 0, false
}
