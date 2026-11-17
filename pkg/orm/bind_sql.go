package orm

import (
	"strconv"
)

// ─── where a bound value may stand: a light postgres tokenizer ───────────
//
// A binding render (bind.go) writes `$k` where bun would have written the
// literal of the k-th value. Before that text is sent, this file answers two
// questions about it, from the tokens alone:
//
//  1. Is it the same statement? Every `$k` must be a parameter token — not
//     text inside a string, comment or dollar-quote, which is where a value
//     nested in an array or JSON literal would land — appearing in order,
//     and substituting each value's literal back must reproduce, byte for
//     byte, the statement bun rendered. Anything else and the statement runs
//     as bun rendered it. That is the guarantee: a bound statement is the
//     inline one with its value literals turned into parameters, and nothing
//     else changed.
//
//  2. Is each position one where a parameter means what the literal meant?
//     Postgres resolves an untyped literal and an untyped parameter the same
//     way in nearly every context. The exceptions, each kept inline:
//
//     - A positional ORDER BY / GROUP BY / DISTINCT ON item: `ORDER BY 2`
//     sorts by the second column, `ORDER BY $1` by a constant.
//     - A typed-literal prefix: `INTERVAL '1 day'` is syntax, `INTERVAL $1`
//     is a syntax error. Any word before the value other than an
//     expression keyword (AND, =, THEN, LIMIT, …) keeps it inline.
//     - A bare argument of a function call, for a text value: a function
//     taking "any" (jsonb_build_object, concat, format) accepts the literal
//     'k' but cannot infer a type for $1.
//     - `<value> IS NULL`, for a text value: nothing gives $1 a type.
//     - Inside ON CONFLICT … DO: a partial unique index is matched against
//     the conflict target's predicate at plan time, which a parameter
//     cannot satisfy.
//
//     A number is bound with the type postgres gives its literal (int4,
//     int8 or numeric, by value) so `data -> 0` stays an array index and a
//     numeric argument resolves its function overload exactly as the
//     literal did.
//
// A statement other than SELECT/INSERT/UPDATE/DELETE/WITH/VALUES/MERGE/TABLE
// (DDL, SET, …), or text holding more than one statement, binds nothing.

type tokKind uint8

const (
	tokWord   tokKind = iota + 1 // identifier or keyword, unquoted
	tokQIdent                    // "quoted identifier"
	tokString                    // any string constant, prefixed or dollar-quoted
	tokNumber
	tokParam // $k
	tokOp    // operator, including ::
	tokOpen  // ( or [
	tokClose // ) or ]
	tokComma
	tokSemi
	tokOther // . : and anything unrecognised
)

// token is one lexeme of the statement. word holds an upper-cased keyword
// candidate (empty when the word is too long to be one).
type token struct {
	kind       tokKind
	start, end int
	word       string
}

// maxKeyword bounds the words compared against keywords; nothing this file
// tests for is longer.
const maxKeyword = 12

// lexer walks postgres SQL text. Its zero value is not usable; see newLexer.
type lexer struct {
	s   []byte
	pos int
	bad bool // an unterminated string, comment or dollar-quote
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c == '$' || (c >= '0' && c <= '9')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isOpChar(c byte) bool {
	switch c {
	case '+', '-', '*', '/', '<', '>', '=', '~', '!', '@', '#', '%', '^', '&', '|', '`', '?':
		return true
	}
	return false
}

// next returns the next significant token; ok is false at the end of the
// text.
func (l *lexer) next() (token, bool) {
	s := l.s
	for l.pos < len(s) {
		c := s[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			l.pos++
		case c == '-' && l.pos+1 < len(s) && s[l.pos+1] == '-':
			for l.pos < len(s) && s[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && l.pos+1 < len(s) && s[l.pos+1] == '*':
			l.skipBlockComment()
		default:
			return l.lex(), true
		}
	}
	return token{}, false
}

func (l *lexer) skipBlockComment() {
	depth := 0
	s := l.s
	for l.pos < len(s) {
		switch {
		case s[l.pos] == '/' && l.pos+1 < len(s) && s[l.pos+1] == '*':
			depth++
			l.pos += 2
		case s[l.pos] == '*' && l.pos+1 < len(s) && s[l.pos+1] == '/':
			depth--
			l.pos += 2
			if depth == 0 {
				return
			}
		default:
			l.pos++
		}
	}
	l.bad = true
}

func (l *lexer) lex() token {
	s := l.s
	start := l.pos
	c := s[start]
	switch {
	case c == '\'':
		l.skipQuoted('\'', false)
		return token{kind: tokString, start: start, end: l.pos}
	case c == '"':
		l.skipQuoted('"', false)
		return token{kind: tokQIdent, start: start, end: l.pos}
	case isIdentStart(c):
		for l.pos < len(s) && isIdentChar(s[l.pos]) {
			l.pos++
		}
		word := s[start:l.pos]
		// A constant prefix glued to its quote: E'…', B'…', X'…', N'…',
		// U&'…' and U&"…".
		if l.pos < len(s) && s[l.pos] == '\'' && len(word) == 1 {
			escapes := word[0]|0x20 == 'e'
			l.skipQuoted('\'', escapes)
			return token{kind: tokString, start: start, end: l.pos}
		}
		if len(word) == 1 && word[0]|0x20 == 'u' && l.pos+1 < len(s) && s[l.pos] == '&' && (s[l.pos+1] == '\'' || s[l.pos+1] == '"') {
			l.pos++
			q := s[l.pos]
			l.skipQuoted(q, false)
			if q == '"' {
				return token{kind: tokQIdent, start: start, end: l.pos}
			}
			return token{kind: tokString, start: start, end: l.pos}
		}
		return token{kind: tokWord, start: start, end: l.pos, word: upperWord(word)}
	case isDigit(c) || (c == '.' && start+1 < len(s) && isDigit(s[start+1])):
		for l.pos < len(s) {
			d := s[l.pos]
			if isDigit(d) || d == '.' || d == '_' || isIdentStart(d) {
				l.pos++
				continue
			}
			// An exponent sign: 1e-5.
			if (d == '+' || d == '-') && (s[l.pos-1]|0x20 == 'e') {
				l.pos++
				continue
			}
			break
		}
		return token{kind: tokNumber, start: start, end: l.pos}
	case c == '$':
		if start+1 < len(s) && isDigit(s[start+1]) {
			l.pos++
			for l.pos < len(s) && isDigit(s[l.pos]) {
				l.pos++
			}
			return token{kind: tokParam, start: start, end: l.pos}
		}
		l.skipDollarQuote()
		return token{kind: tokString, start: start, end: l.pos}
	case c == '(' || c == '[':
		l.pos++
		return token{kind: tokOpen, start: start, end: l.pos}
	case c == ')' || c == ']':
		l.pos++
		return token{kind: tokClose, start: start, end: l.pos}
	case c == ',':
		l.pos++
		return token{kind: tokComma, start: start, end: l.pos}
	case c == ';':
		l.pos++
		return token{kind: tokSemi, start: start, end: l.pos}
	case c == ':':
		l.pos++
		if l.pos < len(s) && s[l.pos] == ':' {
			l.pos++
			return token{kind: tokOp, start: start, end: l.pos}
		}
		return token{kind: tokOther, start: start, end: l.pos}
	case isOpChar(c):
		for l.pos < len(s) && isOpChar(s[l.pos]) {
			// A comment starts inside an operator run: `a+--x`.
			if l.pos > start && (s[l.pos] == '-' && l.pos+1 < len(s) && s[l.pos+1] == '-' ||
				s[l.pos] == '/' && l.pos+1 < len(s) && s[l.pos+1] == '*') {
				break
			}
			l.pos++
		}
		return token{kind: tokOp, start: start, end: l.pos}
	default:
		l.pos++
		return token{kind: tokOther, start: start, end: l.pos}
	}
}

// skipQuoted consumes a quoted run from the opening quote at l.pos: a
// doubled quote is one quote character, and with escapes (E'…') a backslash
// escapes the next byte.
func (l *lexer) skipQuoted(q byte, escapes bool) {
	s := l.s
	l.pos++ // the opening quote
	for l.pos < len(s) {
		c := s[l.pos]
		switch {
		case escapes && c == '\\':
			l.pos += 2
		case c == q:
			if l.pos+1 < len(s) && s[l.pos+1] == q {
				l.pos += 2
				continue
			}
			l.pos++
			return
		default:
			l.pos++
		}
	}
	l.bad = true
}

// skipDollarQuote consumes $tag$…$tag$ from the `$` at l.pos.
func (l *lexer) skipDollarQuote() {
	s := l.s
	start := l.pos
	end := start + 1
	for end < len(s) && s[end] != '$' && isIdentChar(s[end]) {
		end++
	}
	if end >= len(s) || s[end] != '$' || (end > start+1 && isDigit(s[start+1])) {
		// A lone `$`: not valid outside a quote; let it be junk.
		l.pos = start + 1
		return
	}
	tag := s[start : end+1]
	l.pos = end + 1
	for i := l.pos; i+len(tag) <= len(s); i++ {
		if s[i] == '$' && string(s[i:i+len(tag)]) == string(tag) {
			l.pos = i + len(tag)
			return
		}
	}
	l.pos = len(s)
	l.bad = true
}

// upperWord upper-cases an ASCII keyword candidate; a longer or non-ASCII
// word is no keyword this file knows, and comes back empty.
func upperWord(w []byte) string {
	if len(w) > maxKeyword {
		return ""
	}
	var buf [maxKeyword]byte
	for i, c := range w {
		if c >= 0x80 {
			return ""
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		buf[i] = c
	}
	return string(buf[:len(w)])
}

// ─── keyword classes ──────────────────────────────────────────────────────

// dmlStarts are the statements that take bind parameters.
func isDMLStart(w string) bool {
	switch w {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "VALUES", "MERGE", "TABLE":
		return true
	}
	return false
}

// valueKeyword reports whether a value may follow the keyword w directly as
// an expression: anything else before a value (INTERVAL, DATE, a type name)
// makes it a typed literal, which a parameter cannot stand in for.
func valueKeyword(w string) bool {
	switch w {
	case "AND", "OR", "NOT", "LIKE", "ILIKE", "SIMILAR", "TO", "THEN", "ELSE", "WHEN", "CASE",
		"BETWEEN", "SYMMETRIC", "ASYMMETRIC", "SELECT", "DISTINCT", "ALL", "ANY", "SOME",
		"RETURNING", "LIMIT", "OFFSET", "WHERE", "HAVING", "ON", "SET", "FROM", "ESCAPE",
		"ZONE", "USING", "IN", "VALUES", "BY", "FIRST", "NEXT", "DO", "OVERLAPS", "EXISTS",
		"ARRAY", "AS":
		return true
	}
	return false
}

// groupingKeyword reports whether a parenthesis after w groups rather than
// calls a function: `IN (…)`, `VALUES (…)`, `ANY (…)`, `CAST (… AS t)`, and
// the forms (COALESCE, NULLIF, GREATEST, LEAST) postgres resolves the way it
// resolves CASE. A parenthesis after any other word is a function call.
func groupingKeyword(w string) bool {
	switch w {
	case "IN", "VALUES", "ANY", "ALL", "SOME", "EXISTS", "CAST", "COALESCE", "NULLIF",
		"GREATEST", "LEAST", "ON", "USING", "AND", "OR", "NOT", "WHERE", "HAVING", "SELECT",
		"FROM", "JOIN", "AS", "WHEN", "THEN", "ELSE", "CASE", "BY", "RETURNING", "SET",
		"LIMIT", "OFFSET", "BETWEEN", "LIKE", "ILIKE", "IS", "TO", "OVER", "FILTER",
		"LATERAL", "UNION", "EXCEPT", "INTERSECT", "ARRAY", "DO", "WITH", "DISTINCT",
		"CONFLICT", "RECURSIVE", "MATERIALIZED", "ESCAPE", "SYMMETRIC", "ROLLUP", "CUBE",
		"SETS", "WINDOW", "PARTITION":
		return true
	}
	return false
}

// listEnds are the keywords that end an ORDER BY / GROUP BY list at its
// depth.
func listEnds(w string) bool {
	switch w {
	case "LIMIT", "OFFSET", "FETCH", "FOR", "HAVING", "WINDOW", "UNION", "INTERSECT",
		"EXCEPT", "RETURNING", "ON", "WHERE", "FROM", "SELECT", "ROWS", "RANGE", "GROUPS":
		return true
	}
	return false
}

// ─── the scan ────────────────────────────────────────────────────────────

// frame is one level of parentheses or brackets.
type frame struct {
	fn         bool // the parenthesis of a function call
	positional bool // an ORDER BY / GROUP BY list, or DISTINCT ON (…), is open at this depth
	conflict   bool // ON CONFLICT … DO is open at this depth
}

// paramSite is where one `$k` stands.
type paramSite struct {
	start, end int
	prev, next token // the significant tokens around it (kind 0: none)
	fnArg      bool  // directly inside a function call's parentheses
	positional bool  // at the depth of an open positional list
	conflict   bool  // inside ON CONFLICT … DO
}

// bare reports whether the value is a whole list item: nothing joins it to a
// neighbour.
func (p *paramSite) bare() bool {
	return (p.prev.kind == tokOpen || p.prev.kind == tokComma || p.prev.kind == tokWord && p.prev.word == "BY") &&
		(p.next.kind == 0 || p.next.kind == tokClose || p.next.kind == tokComma || p.next.kind == tokSemi || p.next.kind == tokWord)
}

// scanParams finds every parameter token in text, in order, with its
// context. ok is false when the text is not one bindable statement.
func scanParams(text []byte, sites []paramSite) ([]paramSite, bool) {
	l := lexer{s: text}
	stack := make([]frame, 1, 8)
	var (
		prev, prev2 token
		first       = true
		ended       bool // a top-level ';' was seen
		pending     = -1 // the site whose next token is not yet known
	)
	for {
		tok, more := l.next()
		if !more {
			break
		}
		if ended {
			return nil, false // a second statement
		}
		if pending >= 0 {
			sites[pending].next = tok
			pending = -1
		}
		top := &stack[len(stack)-1]
		switch tok.kind {
		case tokWord:
			if first {
				if !isDMLStart(tok.word) {
					return nil, false
				}
				first = false
			}
			switch {
			case tok.word == "BY" && prev.kind == tokWord && (prev.word == "ORDER" || prev.word == "GROUP"):
				top.positional = true
			case tok.word == "CONFLICT" && prev.kind == tokWord && prev.word == "ON":
				top.conflict = true
			case tok.word == "DO" && top.conflict:
				top.conflict = false
			case listEnds(tok.word):
				top.positional = false
			}
		case tokOpen:
			if first {
				break // a parenthesized statement: (SELECT …) UNION (…)
			}
			f := frame{}
			switch prev.kind {
			case tokWord:
				f.fn = !groupingKeyword(prev.word)
				// DISTINCT ON (a, b): the list is positional.
				f.positional = prev.word == "ON" && prev2.kind == tokWord && prev2.word == "DISTINCT"
			case tokQIdent:
				f.fn = true // "schema"."fn"(…), or a quoted column list
			}
			stack = append(stack, f)
		case tokClose:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case tokSemi:
			if len(stack) == 1 {
				ended = true
			}
		case tokParam:
			conflict := false
			for i := range stack {
				conflict = conflict || stack[i].conflict
			}
			sites = append(sites, paramSite{
				start: tok.start, end: tok.end, prev: prev,
				fnArg:      top.fn,
				positional: top.positional,
				conflict:   conflict,
			})
			pending = len(sites) - 1
		default:
			if first && tok.kind != tokOpen {
				return nil, false
			}
		}
		prev2, prev = prev, tok
	}
	if l.bad || first {
		return nil, false
	}
	return sites, true
}

// keepInline reports whether the value at p must stay a literal.
func keepInline(p *paramSite, class paramClass) bool {
	if p.conflict {
		return true
	}
	if p.positional && p.bare() {
		return true
	}
	if p.prev.kind == tokWord && !valueKeyword(p.prev.word) {
		return true // a typed literal: INTERVAL '1 day'
	}
	if class == classNumber {
		return false // typed by its cast
	}
	if p.next.kind == tokWord && (p.next.word == "IS" || p.next.word == "ISNULL" || p.next.word == "NOTNULL") {
		return true
	}
	return p.fnArg && (p.prev.kind == tokOpen || p.prev.kind == tokComma) &&
		(p.next.kind == tokClose || p.next.kind == tokComma)
}

// paramIndex is the k of the parameter token `$k`.
func paramIndex(tok []byte) int {
	n, err := strconv.Atoi(string(tok[1:]))
	if err != nil {
		return -1
	}
	return n
}
