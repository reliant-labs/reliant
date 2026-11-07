package closedset

import (
	"fmt"
	"strings"
)

// List is the closed value list a CHECK expression holds.
type List struct {
	// Column is the column the list constrains, as postgres resolves it.
	Column string
	// Form is the SQL shape the list was read from: FormContainedBy,
	// FormAny or FormIn.
	Form string
	// Values are the list's members, in the order the CHECK spells them.
	Values []string
	// Start and End are the byte span, in the expression, that holds the
	// members (first member's start to last member's end, casts included).
	// Replacing it rewrites the list and leaves the rest of the expression —
	// `cardinality(scopes) > 0 AND …`, the trailing `::text[]` — verbatim.
	Start, End int
	// Cast is the per-member cast every member carries (`::text`,
	// `::character varying`), "" when the members carry none or differ. A
	// rewritten list repeats it, so a pg_dump-normalized CHECK stays in the
	// form pg_dump would write.
	Cast string
	// ArrayLiteral is set when the members came from one array-literal
	// string (`'{a,b}'::text[]`) rather than ARRAY[…] or a list; the span is
	// then the string literal itself.
	ArrayLiteral bool
	// Multiline is set when the CHECK puts its members on separate lines,
	// and Indent is the whitespace before the first one, so a rewritten
	// list keeps the layout.
	Multiline bool
	Indent    string
}

// The value-list shapes ParseCheck reads. Each is how a CHECK says "this
// column holds only these values": the array-containment form for an array
// column, and the two postgres treats as one for a scalar (it normalizes
// `IN (…)` to `= ANY (ARRAY[…])` in pg_get_constraintdef and pg_dump).
const (
	FormContainedBy = "<@ ARRAY[…]"
	FormAny         = "= ANY (ARRAY[…])"
	FormIn          = "IN (…)"
)

// ParseCheck reads the one closed value list in a CHECK expression — the text
// inside `CHECK ( … )`, as a migration or pg_get_constraintdef spells it.
//
// It reads `col <@ ARRAY[…]` (an array column), `col = ANY (ARRAY[…])` and
// `col IN (…)`, with or without casts on the members, the array or the column
// (`(status)::text = ANY ((ARRAY['a'::character varying])::text[])` is
// pg_dump's spelling of a varchar IN), and `'{a,b}'` array literals in place
// of ARRAY[…]. Anything around the list is allowed: `cardinality(scopes) > 0
// AND scopes <@ …`, `kind IS NULL OR kind = ANY (…)`.
//
// An expression with no such list, or with more than one (forge cannot tell
// which the set describes), is an error that says so. A NOT IN list is a deny
// list, not a closed set, and is not read.
func ParseCheck(expr string) (List, error) {
	toks, err := lex(expr)
	if err != nil {
		return List{}, err
	}
	var found []List
	for i, t := range toks {
		var (
			l   List
			ok  bool
			err error
		)
		switch {
		case t.is("<@"):
			l, ok, err = parseArrayOperand(expr, toks, i+1)
			l.Form = FormContainedBy
		case t.is("=") && i+1 < len(toks) && toks[i+1].isWord("any"):
			l, ok, err = parseArrayOperand(expr, toks, i+2)
			l.Form = FormAny
		case t.isWord("in") && (i == 0 || !toks[i-1].isWord("not")):
			l, ok, err = parseInList(expr, toks, i+1)
			l.Form = FormIn
		}
		if err != nil {
			return List{}, err
		}
		if !ok {
			continue
		}
		l.Column = columnBefore(toks, i)
		found = append(found, l)
	}
	switch len(found) {
	case 0:
		return List{}, fmt.Errorf("the CHECK holds no closed value list forge reads — it reads `col <@ ARRAY[…]`, `col = ANY (ARRAY[…])` and `col IN (…)`")
	case 1:
		return found[0], nil
	}
	var cols []string
	for _, l := range found {
		cols = append(cols, l.Column)
	}
	return List{}, fmt.Errorf("the CHECK holds %d value lists (on %s), and a closed-set declaration describes one — split the CHECK so the declared one lists its set alone", len(found), strings.Join(cols, ", "))
}

// parseArrayOperand reads the array an operator compares with, starting at
// toks[i]: ARRAY[…] or an array-literal string, behind any number of opening
// parentheses. ok is false when the operand is neither (a subquery, a
// column), which is not an error: the CHECK simply has no list there.
func parseArrayOperand(expr string, toks []token, i int) (List, bool, error) {
	for i < len(toks) && toks[i].is("(") {
		i++
	}
	if i >= len(toks) {
		return List{}, false, nil
	}
	if toks[i].kind == tString {
		vals := parseArrayLiteral(toks[i].val)
		if vals == nil {
			return List{}, false, nil
		}
		return List{Values: vals, Start: toks[i].start, End: toks[i].end, ArrayLiteral: true}, true, nil
	}
	if !toks[i].isWord("array") || i+1 >= len(toks) || !toks[i+1].is("[") {
		return List{}, false, nil
	}
	return parseMembers(expr, toks, i+2, "]")
}

// parseInList reads `( member, … )` after IN. A subquery (`IN (SELECT …)`) is
// no list.
func parseInList(expr string, toks []token, i int) (List, bool, error) {
	if i >= len(toks) || !toks[i].is("(") {
		return List{}, false, nil
	}
	if i+1 < len(toks) && toks[i+1].isWord("select") {
		return List{}, false, nil
	}
	return parseMembers(expr, toks, i+1, ")")
}

// parseMembers reads literal members, each with an optional cast, up to the
// closing token.
func parseMembers(expr string, toks []token, i int, closer string) (List, bool, error) {
	var (
		l     List
		casts []string
	)
	for n := 1; ; n++ {
		if i >= len(toks) {
			return List{}, false, fmt.Errorf("the CHECK's value list is not closed (no %q)", closer)
		}
		if toks[i].is(closer) && n == 1 {
			return List{}, false, fmt.Errorf("the CHECK's value list is empty")
		}
		m := toks[i]
		if m.kind != tString && m.kind != tNumber {
			return List{}, false, fmt.Errorf("member %d of the CHECK's value list is %q, not a literal — a closed set lists constants", n, m.text)
		}
		if n == 1 {
			l.Start = m.start
			l.Indent = lineIndent(expr, m.start)
		}
		l.Values = append(l.Values, m.val)
		end := m.end
		i++
		cast := ""
		if i < len(toks) && toks[i].is("::") {
			castStart := toks[i].start
			for i < len(toks) && !toks[i].is(",") && !toks[i].is(closer) {
				end = toks[i].end
				i++
			}
			cast = normalizeSpace(expr[castStart:end])
		}
		casts = append(casts, cast)
		l.End = end
		if i >= len(toks) {
			return List{}, false, fmt.Errorf("the CHECK's value list is not closed (no %q)", closer)
		}
		if toks[i].is(closer) {
			break
		}
		if !toks[i].is(",") {
			return List{}, false, fmt.Errorf("unexpected %q in the CHECK's value list", toks[i].text)
		}
		i++
	}
	l.Multiline = strings.Contains(expr[l.Start:l.End], "\n")
	l.Cast = casts[0]
	for _, c := range casts[1:] {
		if c != l.Cast {
			l.Cast = ""
			break
		}
	}
	return l, true, nil
}

// columnBefore names the column an operator at toks[op] constrains: the
// identifier before it, past closing parentheses and casts, so `scopes`,
// `(status)::text` and `(kind)::character varying` all read as the column.
func columnBefore(toks []token, op int) string {
	i := op - 1
	for i >= 0 {
		t := toks[i]
		switch {
		case t.is(")") || t.is("]") || t.is("["):
			i--
		case t.kind == tIdent:
			// A run of words preceded by `::` is a cast's type
			// (`::character varying`): skip it and the `::`.
			j := i
			for j >= 0 && toks[j].kind == tIdent {
				j--
			}
			if j >= 0 && toks[j].is("::") {
				i = j - 1
				continue
			}
			return t.val
		default:
			return ""
		}
	}
	return ""
}

// parseArrayLiteral reads the members of a postgres array literal's text,
// `{a,b,"c d"}`. nil when the text is not one.
func parseArrayLiteral(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil
	}
	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(body, ",") {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		out = append(out, p)
	}
	return out
}

func lineIndent(s string, at int) string {
	start := strings.LastIndexByte(s[:at], '\n') + 1
	prefix := s[start:at]
	if strings.TrimSpace(prefix) != "" {
		return ""
	}
	return prefix
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tNumber
	tPunct
)

type token struct {
	kind tokKind
	// text is the token as written; val is its value — an identifier
	// folded as postgres folds it, a string without its quotes.
	text, val  string
	start, end int
}

func (t token) is(p string) bool { return t.kind == tPunct && t.text == p }
func (t token) isWord(w string) bool {
	return t.kind == tIdent && !strings.HasPrefix(t.text, `"`) && t.val == w
}
func isIdentStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIdentByte(c byte) bool  { return isIdentStart(c) || (c >= '0' && c <= '9') || c == '$' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func tok(k tokKind, s string, i, j int) token {
	return token{kind: k, text: s[i:j], val: s[i:j], start: i, end: j}
}

// lex tokenizes a CHECK expression the way postgres reads one: comments are
// skipped, a string constant is one token (a doubled quote inside it is one
// quote), and a quoted identifier keeps its case.
func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case strings.HasPrefix(s[i:], "--"):
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(s)
			}
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return nil, fmt.Errorf("unterminated comment in the CHECK")
			}
			i += j + 4
		case c == '\'' || c == '"':
			var b strings.Builder
			j := i + 1
			for {
				if j >= len(s) {
					return nil, fmt.Errorf("unterminated %c in the CHECK", c)
				}
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						b.WriteByte(c)
						j += 2
						continue
					}
					break
				}
				b.WriteByte(s[j])
				j++
			}
			t := tok(tString, s, i, j+1)
			if c == '"' {
				t.kind = tIdent
			}
			t.val = b.String()
			out = append(out, t)
			i = j + 1
		case isIdentStart(c):
			j := i + 1
			for j < len(s) && isIdentByte(s[j]) {
				j++
			}
			t := tok(tIdent, s, i, j)
			t.val = strings.ToLower(t.text)
			out = append(out, t)
			i = j
		case isDigit(c) || (c == '-' && i+1 < len(s) && isDigit(s[i+1]) && precedesOperand(out)):
			j := i + 1
			for j < len(s) && (isDigit(s[j]) || s[j] == '.') {
				j++
			}
			out = append(out, tok(tNumber, s, i, j))
			i = j
		default:
			n := 1
			for _, op := range []string{"::", "<@", "@>", "<>", "!=", "<=", ">=", "||"} {
				if strings.HasPrefix(s[i:], op) {
					n = 2
					break
				}
			}
			out = append(out, tok(tPunct, s, i, i+n))
			i += n
		}
	}
	return out, nil
}

// precedesOperand reports whether a `-` here is a sign rather than an
// operator: at the start, or after an opening bracket or a comma.
func precedesOperand(toks []token) bool {
	if len(toks) == 0 {
		return true
	}
	last := toks[len(toks)-1]
	return last.is("(") || last.is("[") || last.is(",")
}
