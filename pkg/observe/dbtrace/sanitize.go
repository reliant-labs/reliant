package dbtrace

import (
	"strings"
	"unicode"
)

// maxQueryText caps db.query.text. A statement longer than this is almost
// always a generated multi-row VALUES list, whose tail says nothing new.
const maxQueryText = 2048

// Dialect is the set of lexical rules a statement is read by: where its
// string literals, quoted identifiers and comments start and end. It matters
// for more than accuracy. Read by another dialect's rules, a literal can end
// early, and the rest of it is recorded as SQL text: Postgres ends
// 'a\'secret' at the backslash-quote, ClickHouse does not.
type Dialect int

const (
	// Postgres: a backslash escapes only inside E'…'; "…" quotes an
	// identifier; $tag$…$tag$ is a literal and $1 a placeholder.
	Postgres Dialect = iota
	// ClickHouse: a backslash escapes inside every quoted string and
	// identifier; `…` and “…” also quote an identifier and ‘…’ is a literal;
	// //, "# " and #! also start a line comment; $tag$…$tag$ is a heredoc.
	ClickHouse
)

// SanitizeSQL is Postgres.Sanitize.
func SanitizeSQL(query string) string { return Postgres.Sanitize(query) }

// Summarize is Postgres.Summarize.
func Summarize(query string) (operation, table string) { return Postgres.Summarize(query) }

// Sanitize returns query with every literal replaced by ?, so the text can
// be recorded on a span without carrying the data it names: string literals
// ('…', E'…', $tag$…$tag$, and the dialect's own forms) and numeric literals
// become ?. Placeholders ($1, {name:Type}), identifiers (including quoted ones
// and digits inside names) and keywords are kept. Comments are dropped, runs
// of whitespace collapse to one space, and the result is capped at
// maxQueryText bytes.
//
// It is a lexer, not a parser: it does not need to understand the statement,
// only where literals start and end, which the dialect's lexical rules fix.
func (d Dialect) Sanitize(query string) string {
	var b strings.Builder
	b.Grow(min(len(query), maxQueryText))
	space := false
	emit := func(s string) {
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteString(s)
	}
	for i := 0; i < len(query) && b.Len() < maxQueryText; {
		if kind, j := d.quotedOrComment(query, i); kind != plain {
			switch kind {
			case commentTok:
				space = true
			case literalTok:
				emit("?")
			case quotedIdentTok:
				emit(query[i:j])
			}
			i = j
			continue
		}
		c := query[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space = true
			i++
		case c == '$':
			j := i + 1
			for j < len(query) && isDigit(query[j]) {
				j++
			}
			emit(query[i:j]) // a $1 placeholder, or a lone $
			i = j
		case isDigit(c) || (c == '.' && i+1 < len(query) && isDigit(query[i+1])):
			i = skipNumber(query, i)
			emit("?")
		case isIdentStart(c):
			j := d.identEnd(query, i)
			emit(query[i:j])
			i = j
		default:
			emit(query[i : i+1])
			i++
		}
	}
	out := b.String()
	if len(out) > maxQueryText {
		out = out[:maxQueryText]
	}
	return out
}

// Summarize names a statement for a span: its operation (SELECT, INSERT, …,
// upper-cased) and the table it targets, when that is a plain name. A WITH
// statement is named for the statement its CTEs feed. Either may be empty.
func (d Dialect) Summarize(query string) (operation, table string) {
	toks := d.topLevelTokens(query, 64)
	if len(toks) == 0 {
		return "", ""
	}
	start := 0
	if strings.EqualFold(toks[0], "WITH") {
		start = -1
		for i := 1; i < len(toks); i++ {
			switch strings.ToUpper(toks[i]) {
			case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE":
				start = i
			}
			if start >= 0 {
				break
			}
		}
		if start < 0 {
			return "WITH", ""
		}
	}
	operation = strings.ToUpper(toks[start])
	if !isWord(operation) {
		return "", ""
	}
	after := func(keyword string) string {
		for i := start + 1; i+1 < len(toks); i++ {
			if strings.EqualFold(toks[i], keyword) {
				return tableName(toks[i+1])
			}
		}
		return ""
	}
	switch operation {
	case "SELECT", "DELETE":
		table = after("FROM")
	case "INSERT", "MERGE":
		table = after("INTO")
	case "UPDATE":
		if start+1 < len(toks) {
			next := toks[start+1]
			if strings.EqualFold(next, "ONLY") && start+2 < len(toks) {
				next = toks[start+2]
			}
			table = tableName(next)
		}
	}
	return operation, table
}

// topLevelTokens returns up to limit words and punctuation at parenthesis
// depth zero, skipping literals and comments. A parenthesised group is one
// "(" token; a qualified name (schema.table, quoted parts included) is one
// token.
func (d Dialect) topLevelTokens(query string, limit int) []string {
	var toks []string
	depth := 0
	for i := 0; i < len(query) && len(toks) < limit; {
		if kind, j := d.quotedOrComment(query, i); kind == commentTok || kind == literalTok {
			i = j
			continue
		}
		c := query[i]
		switch {
		case c == '(':
			if depth == 0 {
				toks = append(toks, "(")
			}
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case isIdentStart(c) || d.opensQuotedIdent(query, i):
			j := d.skipName(query, i)
			if depth == 0 {
				toks = append(toks, query[i:j])
			}
			i = j
		default:
			i++
		}
	}
	return toks
}

// skipName returns the index past the (optionally qualified) name at i: words
// and quoted identifiers joined by dots.
func (d Dialect) skipName(q string, i int) int {
	for i < len(q) {
		switch {
		case d.opensQuotedIdent(q, i):
			_, i = d.quotedOrComment(q, i)
		case isIdentStart(q[i]):
			i = d.identEnd(q, i)
		default:
			return i
		}
		if i >= len(q) || q[i] != '.' {
			return i
		}
		i++
	}
	return i
}

// identEnd returns the index past the unquoted identifier starting at i.
// Every byte of a multi-byte character continues one, except that in
// ClickHouse a ‘ or “ opens a quote even directly after a name (x‘…’).
func (d Dialect) identEnd(q string, i int) int {
	j := i + 1
	for j < len(q) && isIdentPart(q[j]) {
		if d == ClickHouse && (strings.HasPrefix(q[j:], leftSingleQuote) || strings.HasPrefix(q[j:], leftDoubleQuote)) {
			break
		}
		j++
	}
	return j
}

// tokKind is what quotedOrComment found.
type tokKind int

const (
	plain tokKind = iota
	commentTok
	literalTok
	quotedIdentTok
)

const (
	leftSingleQuote  = "\u2018" // ‘
	rightSingleQuote = "\u2019" // ’
	leftDoubleQuote  = "\u201c" // “
	rightDoubleQuote = "\u201d" // ”
)

// quotedOrComment reports whether a comment, a string literal or a quoted
// identifier starts at q[i], and if so the index just past it. An
// unterminated one runs to the end of the statement, so its text is never
// read as SQL.
func (d Dialect) quotedOrComment(q string, i int) (tokKind, int) {
	c := q[i]
	var next byte
	if i+1 < len(q) {
		next = q[i+1]
	}
	ch := d == ClickHouse
	switch {
	case c == '-' && next == '-',
		ch && c == '/' && next == '/',
		ch && c == '#' && (next == ' ' || next == '!'):
		if j := strings.IndexByte(q[i:], '\n'); j >= 0 {
			return commentTok, i + j
		}
		return commentTok, len(q)
	case c == '/' && next == '*':
		return commentTok, skipBlockComment(q, i)
	case c == '\'':
		return literalTok, skipQuoted(q, i, ch)
	case !ch && (c == 'E' || c == 'e') && next == '\'' && !identBefore(q, i):
		return literalTok, skipQuoted(q, i+1, true)
	case c == '"' || (ch && c == '`'):
		return quotedIdentTok, skipQuoted(q, i, ch)
	case ch && strings.HasPrefix(q[i:], leftSingleQuote):
		return literalTok, skipPast(q, i+len(leftSingleQuote), rightSingleQuote)
	case ch && strings.HasPrefix(q[i:], leftDoubleQuote):
		return quotedIdentTok, skipPast(q, i+len(leftDoubleQuote), rightDoubleQuote)
	case c == '$':
		if j, ok := d.dollarQuoteEnd(q, i); ok {
			return literalTok, j
		}
	}
	return plain, i
}

// opensQuotedIdent reports whether a quoted identifier starts at q[i].
func (d Dialect) opensQuotedIdent(q string, i int) bool {
	kind, _ := d.quotedOrComment(q, i)
	return kind == quotedIdentTok
}

// tableName returns tok as a table name — quotes removed, schema kept — or ""
// when it is not a plain (optionally qualified) identifier.
func tableName(tok string) string {
	name := strings.NewReplacer(`"`, "", "`", "", leftDoubleQuote, "", rightDoubleQuote, "").Replace(tok)
	if name == "" || !isIdentStart(name[0]) {
		return ""
	}
	for i := 0; i < len(name); i++ {
		if !isIdentPart(name[i]) && name[i] != '.' {
			return ""
		}
	}
	return name
}

func isWord(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return s != ""
}

func skipBlockComment(q string, i int) int {
	// Block comments nest, in postgres and in ClickHouse.
	depth := 0
	for i < len(q) {
		switch {
		case q[i] == '/' && i+1 < len(q) && q[i+1] == '*':
			depth++
			i += 2
		case q[i] == '*' && i+1 < len(q) && q[i+1] == '/':
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return i
}

// skipQuoted returns the index just past the quoted string or identifier
// opening at i, whose quote character is q[i]. A doubled quote is an escaped
// quote; with backslashes (a Postgres E-string, anything ClickHouse quotes)
// so is a backslash-quote.
func skipQuoted(q string, i int, backslashes bool) int {
	quote := q[i]
	i++ // opening quote
	for i < len(q) {
		switch {
		case backslashes && q[i] == '\\':
			i += 2
		case q[i] == quote:
			if i+1 < len(q) && q[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return len(q)
}

// skipPast returns the index just past the first end at or after i, or the
// end of q.
func skipPast(q string, i int, end string) int {
	if j := strings.Index(q[i:], end); j >= 0 {
		return i + j + len(end)
	}
	return len(q)
}

// dollarQuoteEnd reports the index past a $tag$…$tag$ literal opening at i.
// In Postgres a tag cannot start with a digit ($1 is a placeholder); a
// ClickHouse heredoc tag is any run of word characters.
func (d Dialect) dollarQuoteEnd(q string, i int) (int, bool) {
	j := i + 1
	if d == Postgres && j < len(q) && isDigit(q[j]) {
		return 0, false
	}
	for j < len(q) && d.dollarTagPart(q[j]) {
		j++
	}
	if j >= len(q) || q[j] != '$' {
		return 0, false
	}
	tag := q[i : j+1]
	end := strings.Index(q[j+1:], tag)
	if end < 0 {
		return len(q), true
	}
	return j + 1 + end + len(tag), true
}

func (d Dialect) dollarTagPart(c byte) bool {
	if d == ClickHouse {
		return c == '_' || isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'z')
	}
	return isIdentStart(c) || isDigit(c) // a tag has no $
}

// skipNumber returns the index past the numeric literal at i: decimal, with
// a fraction and exponent, or 0x hex (with a p exponent), 0b binary or 0o
// octal, any of them with _ separators.
func skipNumber(q string, i int) int {
	if q[i] == '0' && i+2 < len(q) {
		switch q[i+1] | 0x20 {
		case 'x':
			if isHexDigit(q[i+2]) {
				i += 2
				for i < len(q) && (isHexDigit(q[i]) || q[i] == '.' || q[i] == '_') {
					i++
				}
				return skipExponent(q, i, 'p')
			}
		case 'b', 'o':
			if isDigit(q[i+2]) {
				i += 2
				for i < len(q) && (isDigit(q[i]) || q[i] == '_') {
					i++
				}
				return i
			}
		}
	}
	for i < len(q) && (isDigit(q[i]) || q[i] == '.' || q[i] == '_') {
		i++
	}
	return skipExponent(q, i, 'e')
}

// skipExponent consumes an exponent marked by e (or its upper case) at i.
func skipExponent(q string, i int, e byte) int {
	if i < len(q) && q[i]|0x20 == e {
		j := i + 1
		if j < len(q) && (q[j] == '+' || q[j] == '-') {
			j++
		}
		if j < len(q) && isDigit(q[j]) {
			i = j
			for i < len(q) && (isDigit(q[i]) || q[i] == '_') {
				i++
			}
		}
	}
	return i
}

func identBefore(q string, i int) bool { return i > 0 && isIdentPart(q[i-1]) }

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isHexDigit(c byte) bool   { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }
func isIdentStart(c byte) bool { return c == '_' || c >= 0x80 || unicode.IsLetter(rune(c)) }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '$' }
