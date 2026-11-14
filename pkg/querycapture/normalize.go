package querycapture

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
)

// Statement is one SQL statement normalized for capture.
type Statement struct {
	// SQL is the statement as it was sent, normalized: comments dropped,
	// whitespace collapsed, a list of two or more constants after IN, and
	// the rows after VALUES, cut to the first one, and the $n placeholders
	// renumbered from $1 in order of appearance. It stays a statement
	// Postgres can EXPLAIN (GENERIC_PLAN): literals the application sent
	// inline stay inline (see the package doc).
	SQL string
	// Fingerprint identifies the statement's shape: a hash of SQL with every
	// string and numeric literal masked and unquoted words lower-cased, so
	// the same statement run with different inline values, or spelled with
	// different spacing or keyword case, has one fingerprint.
	Fingerprint string
	// Operation is the statement's verb, upper-cased: SELECT, INSERT,
	// UPDATE, DELETE, MERGE, … For a WITH statement, the verb its CTEs
	// feed. Empty when none is recognizable.
	Operation string
	// Tables are the relations named after FROM, JOIN, INTO and UPDATE,
	// unquoted, schema-qualified when the statement qualifies them, in order
	// of first appearance, CTE names excluded. Informational: a plan names
	// the relations a statement really reads.
	Tables []string
}

// Normalize normalizes query and computes its fingerprint.
func Normalize(query string) Statement {
	toks := collapseValues(collapseInLists(lex(query)))
	for len(toks) > 0 && toks[len(toks)-1].text == ";" {
		toks = toks[:len(toks)-1]
	}
	renumberParams(toks)

	var sqlB, shapeB strings.Builder
	sqlB.Grow(len(query))
	shapeB.Grow(len(query))
	for i, t := range toks {
		if i > 0 {
			if t.space {
				sqlB.WriteByte(' ')
			}
			shapeB.WriteByte(' ')
		}
		sqlB.WriteString(t.text)
		switch t.kind {
		case tokString, tokNumber:
			shapeB.WriteByte('?')
		case tokWord:
			shapeB.WriteString(strings.ToLower(t.text))
		default:
			shapeB.WriteString(t.text)
		}
	}
	sum := sha256.Sum256([]byte(shapeB.String()))
	return Statement{
		SQL:         sqlB.String(),
		Fingerprint: hex.EncodeToString(sum[:8]),
		Operation:   operation(toks),
		Tables:      tables(toks),
	}
}

// Explainable reports whether a statement of this Operation is one EXPLAIN
// plans, and so one capture records: SELECT, INSERT, UPDATE, DELETE, MERGE,
// VALUES and TABLE (a WITH statement reports the verb its CTEs feed).
func Explainable(operation string) bool {
	switch operation {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "VALUES", "TABLE":
		return true
	}
	return false
}

// ─── lexer ────────────────────────────────────────────────────────────────

type tokKind uint8

const (
	tokWord   tokKind = iota // keyword or unquoted identifier
	tokQIdent                // "quoted identifier"
	tokString                // '…', E'…', B'…', X'…', N'…', U&'…', $tag$…$tag$
	tokNumber                // 42, 1.5, .5e3, 0x1F
	tokParam                 // $1
	tokPunct                 // anything else: one byte, or ::
)

type token struct {
	kind  tokKind
	text  string
	space bool // whitespace or a comment preceded it
}

// lex splits a Postgres statement into tokens. It is a lexer, not a parser:
// it needs to know only where literals, identifiers, placeholders and
// comments start and end, which Postgres's lexical rules fix. A
// standard_conforming_strings server is assumed (the default since 9.1): a
// backslash escapes only inside E'…'.
func lex(q string) []token {
	var toks []token
	space := false
	add := func(kind tokKind, text string) {
		toks = append(toks, token{kind: kind, text: text, space: space})
		space = false
	}
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space = true
			i++
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			for i < len(q) && q[i] != '\n' {
				i++
			}
			space = true
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			i = skipBlockComment(q, i)
			space = true
		case c == '\'':
			j := skipQuoted(q, i, '\'', false)
			add(tokString, q[i:j])
			i = j
		case c == '"':
			j := skipQuoted(q, i, '"', false)
			add(tokQIdent, q[i:j])
			i = j
		case c == '$':
			if i+1 < len(q) && isDigit(q[i+1]) {
				j := i + 1
				for j < len(q) && isDigit(q[j]) {
					j++
				}
				add(tokParam, q[i:j])
				i = j
			} else if j, ok := dollarQuoteEnd(q, i); ok {
				add(tokString, q[i:j])
				i = j
			} else {
				add(tokPunct, "$")
				i++
			}
		case isDigit(c) || (c == '.' && i+1 < len(q) && isDigit(q[i+1])):
			j := skipNumber(q, i)
			add(tokNumber, q[i:j])
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(q) && isIdentPart(q[j]) {
				j++
			}
			if j < len(q) && q[j] == '\'' && isStringPrefix(q[i:j]) {
				k := skipQuoted(q, j, '\'', strings.EqualFold(q[i:j], "e"))
				add(tokString, q[i:k])
				i = k
				continue
			}
			if j+1 < len(q) && q[j] == '&' && q[j+1] == '\'' && strings.EqualFold(q[i:j], "u") {
				k := skipQuoted(q, j+1, '\'', false)
				add(tokString, q[i:k])
				i = k
				continue
			}
			add(tokWord, q[i:j])
			i = j
		case c == ':' && i+1 < len(q) && q[i+1] == ':':
			add(tokPunct, "::")
			i += 2
		default:
			add(tokPunct, q[i:i+1])
			i++
		}
	}
	return toks
}

func isStringPrefix(s string) bool {
	switch strings.ToLower(s) {
	case "e", "b", "x", "n":
		return true
	}
	return false
}

// skipQuoted returns the index past the quoted run opening at i. A doubled
// quote is an escaped quote; with backslashes, so is \<quote>. An
// unterminated run ends the statement.
func skipQuoted(q string, i int, quote byte, backslashes bool) int {
	for j := i + 1; j < len(q); j++ {
		switch q[j] {
		case '\\':
			if backslashes {
				j++
			}
		case quote:
			if j+1 < len(q) && q[j+1] == quote {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(q)
}

// skipBlockComment returns the index past the /* … */ comment at i; Postgres
// block comments nest.
func skipBlockComment(q string, i int) int {
	depth := 0
	for i < len(q) {
		switch {
		case strings.HasPrefix(q[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(q[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(q)
}

// dollarQuoteEnd returns the index past the $tag$…$tag$ literal at i.
func dollarQuoteEnd(q string, i int) (int, bool) {
	j := i + 1
	for j < len(q) && q[j] != '$' {
		if !isIdentPart(q[j]) {
			return 0, false
		}
		j++
	}
	if j >= len(q) {
		return 0, false
	}
	tag := q[i : j+1]
	if end := strings.Index(q[j+1:], tag); end >= 0 {
		return j + 1 + end + len(tag), true
	}
	return len(q), true
}

func skipNumber(q string, i int) int {
	if q[i] == '0' && i+1 < len(q) && (q[i+1]|0x20 == 'x' || q[i+1]|0x20 == 'o' || q[i+1]|0x20 == 'b') {
		j := i + 2
		for j < len(q) && (isHexDigit(q[j]) || q[j] == '_') {
			j++
		}
		return j
	}
	j := i
	for j < len(q) && (isDigit(q[j]) || q[j] == '_') {
		j++
	}
	if j < len(q) && q[j] == '.' {
		j++
		for j < len(q) && isDigit(q[j]) {
			j++
		}
	}
	if j < len(q) && q[j]|0x20 == 'e' {
		k := j + 1
		if k < len(q) && (q[k] == '+' || q[k] == '-') {
			k++
		}
		if k < len(q) && isDigit(q[k]) {
			for k < len(q) && isDigit(q[k]) {
				k++
			}
			j = k
		}
	}
	return j
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool   { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }
func isIdentStart(c byte) bool { return c == '_' || c >= 0x80 || unicode.IsLetter(rune(c)) }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '$' }

func isWord(t token, w string) bool { return t.kind == tokWord && strings.EqualFold(t.text, w) }

// ─── list collapsing ──────────────────────────────────────────────────────

// matchParen returns the index of the ")" closing the "(" at i, or -1.
func matchParen(toks []token, i int) int {
	depth := 0
	for j := i; j < len(toks); j++ {
		if toks[j].kind != tokPunct {
			continue
		}
		switch toks[j].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// collapseInLists cuts `IN (c1, c2, …)` to `IN (c1)` when every element is a
// constant: a placeholder or literal, optionally cast. A list's length is
// data, not shape — bun writes one placeholder per element — while
// `IN (SELECT …)` and `IN (a.x, b.y)` are shape and are kept.
func collapseInLists(toks []token) []token {
	out := toks[:0:0]
	for i := 0; i < len(toks); i++ {
		out = append(out, toks[i])
		if !isWord(toks[i], "IN") || i+1 >= len(toks) || toks[i+1].text != "(" || toks[i+1].kind != tokPunct {
			continue
		}
		end := matchParen(toks, i+1)
		if end < 0 {
			continue
		}
		elems := splitTopLevel(toks[i+2 : end])
		if len(elems) < 2 {
			continue
		}
		constant := true
		for _, e := range elems {
			if !isConstant(e) {
				constant = false
				break
			}
		}
		if !constant {
			continue
		}
		out = append(out, toks[i+1])
		out = append(out, elems[0]...)
		out = append(out, toks[end])
		i = end
	}
	return out
}

// collapseValues cuts `VALUES (…), (…), …` to its first row: a multi-row
// INSERT's row count is data, not shape.
func collapseValues(toks []token) []token {
	out := toks[:0:0]
	for i := 0; i < len(toks); i++ {
		out = append(out, toks[i])
		if !isWord(toks[i], "VALUES") || i+1 >= len(toks) || toks[i+1].text != "(" || toks[i+1].kind != tokPunct {
			continue
		}
		end := matchParen(toks, i+1)
		if end < 0 {
			continue
		}
		out = append(out, toks[i+1:end+1]...)
		i = end
		for i+2 < len(toks) && toks[i+1].kind == tokPunct && toks[i+1].text == "," &&
			toks[i+2].kind == tokPunct && toks[i+2].text == "(" {
			next := matchParen(toks, i+2)
			if next < 0 {
				break
			}
			i = next
		}
	}
	return out
}

// splitTopLevel splits toks at the commas outside parentheses.
func splitTopLevel(toks []token) [][]token {
	var out [][]token
	depth, start := 0, 0
	for j, t := range toks {
		if t.kind != tokPunct {
			continue
		}
		switch t.text {
		case "(", "[":
			depth++
		case ")", "]":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, toks[start:j])
				start = j + 1
			}
		}
	}
	return append(out, toks[start:])
}

// isConstant reports whether an IN-list element is a constant: placeholders,
// literals, NULL/TRUE/FALSE, signs and parentheses, and type names after ::.
func isConstant(e []token) bool {
	if len(e) == 0 {
		return false
	}
	value := false
	for j, t := range e {
		switch t.kind {
		case tokParam, tokString, tokNumber:
			value = true
		case tokWord:
			if j > 0 && e[j-1].text == "::" {
				continue
			}
			switch strings.ToUpper(t.text) {
			case "NULL", "TRUE", "FALSE":
				value = true
			default:
				return false
			}
		case tokQIdent:
			return false
		case tokPunct:
			switch t.text {
			case "(", ")", "::", "-", "+", "[", "]":
			default:
				return false
			}
		}
	}
	return value
}

// renumberParams renames the placeholders $1, $2, … in order of first
// appearance. Collapsing a list drops placeholders, and a statement whose
// numbers have a gap cannot be prepared ("could not determine data type of
// parameter $2"), so the survivors are renumbered densely.
func renumberParams(toks []token) {
	seen := map[string]string{}
	for i, t := range toks {
		if t.kind != tokParam {
			continue
		}
		n, ok := seen[t.text]
		if !ok {
			n = "$" + strconv.Itoa(len(seen)+1)
			seen[t.text] = n
		}
		toks[i].text = n
	}
}

// ─── operation and tables ─────────────────────────────────────────────────

var verbs = map[string]bool{"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "MERGE": true}

func operation(toks []token) string {
	if len(toks) == 0 || toks[0].kind != tokWord {
		return ""
	}
	first := strings.ToUpper(toks[0].text)
	if first != "WITH" {
		return first
	}
	depth := 0
	for _, t := range toks[1:] {
		switch {
		case t.kind == tokPunct && t.text == "(":
			depth++
		case t.kind == tokPunct && t.text == ")":
			depth--
		case depth == 0 && t.kind == tokWord && verbs[strings.ToUpper(t.text)]:
			return strings.ToUpper(t.text)
		}
	}
	return "WITH"
}

// fromFunctions are the functions whose argument syntax uses FROM:
// EXTRACT(field FROM ts), SUBSTRING(s FROM 2), … — not a table.
var fromFunctions = map[string]bool{"extract": true, "substring": true, "trim": true, "overlay": true, "position": true}

func tables(toks []token) []string {
	ctes := map[string]bool{}
	for i := 0; i+2 < len(toks); i++ {
		if (toks[i].kind == tokWord || toks[i].kind == tokQIdent) && isWord(toks[i+1], "AS") {
			j := i + 2
			for j < len(toks) && (isWord(toks[j], "NOT") || isWord(toks[j], "MATERIALIZED")) {
				j++
			}
			if j < len(toks) && toks[j].kind == tokPunct && toks[j].text == "(" {
				ctes[identName(toks[i])] = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	var skip []bool // per open paren: inside a FROM-syntax function call
	inFromFunc := func() bool { return len(skip) > 0 && skip[len(skip)-1] }
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tokPunct {
			switch t.text {
			case "(":
				call := i > 0 && toks[i-1].kind == tokWord && fromFunctions[strings.ToLower(toks[i-1].text)]
				skip = append(skip, call || inFromFunc())
			case ")":
				if len(skip) > 0 {
					skip = skip[:len(skip)-1]
				}
			}
			continue
		}
		if t.kind != tokWord || inFromFunc() {
			continue
		}
		kw := strings.ToUpper(t.text)
		switch kw {
		case "FROM", "JOIN", "INTO", "UPDATE":
		default:
			continue
		}
		j := i + 1
		for j < len(toks) && (isWord(toks[j], "ONLY") || isWord(toks[j], "LATERAL")) {
			j++
		}
		name, next := qualifiedName(toks, j)
		// After FROM or JOIN, a name followed by "(" is a function call
		// (generate_series(…)); after INTO it is the column list.
		call := (kw == "FROM" || kw == "JOIN") && next < len(toks) && toks[next].kind == tokPunct && toks[next].text == "("
		if name == "" || ctes[name] || call {
			continue // a subquery, a function, a CTE, or a keyword (DO UPDATE SET)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// sqlKeywords are words that can follow FROM/JOIN/INTO/UPDATE and are not a
// relation.
var sqlKeywords = map[string]bool{"set": true, "select": true, "values": true, "where": true, "default": true}

// qualifiedName reads the possibly schema-qualified relation name at i and
// returns it with the index past it.
func qualifiedName(toks []token, i int) (string, int) {
	var parts []string
	for i < len(toks) && (toks[i].kind == tokWord || toks[i].kind == tokQIdent) {
		if toks[i].kind == tokWord && len(parts) == 0 && sqlKeywords[strings.ToLower(toks[i].text)] {
			return "", i
		}
		parts = append(parts, identName(toks[i]))
		i++
		if i+1 < len(toks) && toks[i].kind == tokPunct && toks[i].text == "." {
			i++
			continue
		}
		break
	}
	return strings.Join(parts, "."), i
}

// identName is an identifier's name: a quoted one unquoted, an unquoted one
// folded to lower case, as Postgres folds it.
func identName(t token) string {
	if t.kind == tokQIdent {
		s := t.text
		if len(s) >= 2 && s[len(s)-1] == '"' {
			s = s[1 : len(s)-1]
		} else {
			s = s[1:]
		}
		return strings.ReplaceAll(s, `""`, `"`)
	}
	return strings.ToLower(t.text)
}
