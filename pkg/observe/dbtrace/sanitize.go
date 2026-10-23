package dbtrace

import (
	"strings"
	"unicode"
)

// maxQueryText caps db.query.text. A statement longer than this is almost
// always a generated multi-row VALUES list, whose tail says nothing new.
const maxQueryText = 2048

// SanitizeSQL returns query with every literal replaced by ?, so the text can
// be recorded on a span without carrying the data it names: string literals
// ('…', E'…', $tag$…$tag$) and numeric literals become ?. Placeholders ($1),
// identifiers (including quoted ones and digits inside names) and keywords are
// kept. Comments are dropped, runs of whitespace collapse to one space, and
// the result is capped at maxQueryText bytes.
//
// It is a lexer, not a parser: it does not need to understand the statement,
// only where literals start and end, which postgres's lexical rules fix.
func SanitizeSQL(query string) string {
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
		c := query[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			space = true
			i++
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			for i < len(query) && query[i] != '\n' {
				i++
			}
			space = true
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			i = skipBlockComment(query, i)
			space = true
		case c == '\'':
			i = skipString(query, i, false)
			emit("?")
		case (c == 'E' || c == 'e') && i+1 < len(query) && query[i+1] == '\'' && !identBefore(query, i):
			i = skipString(query, i+1, true)
			emit("?")
		case c == '"':
			j := skipQuotedIdent(query, i)
			emit(query[i:j])
			i = j
		case c == '$':
			if j, ok := dollarQuoteEnd(query, i); ok {
				i = j
				emit("?")
				continue
			}
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
			j := i + 1
			for j < len(query) && isIdentPart(query[j]) {
				j++
			}
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
func Summarize(query string) (operation, table string) {
	toks := topLevelTokens(query, 64)
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
// "(" token.
func topLevelTokens(query string, limit int) []string {
	var toks []string
	depth := 0
	for i := 0; i < len(query) && len(toks) < limit; {
		c := query[i]
		switch {
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(query) && query[i+1] == '*':
			i = skipBlockComment(query, i)
		case c == '\'':
			i = skipString(query, i, false)
		case c == '$':
			if j, ok := dollarQuoteEnd(query, i); ok {
				i = j
			} else {
				i++
			}
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
		case c == '"':
			j := skipQuotedIdent(query, i)
			if depth == 0 {
				toks = append(toks, query[i:j])
			}
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < len(query) && (isIdentPart(query[j]) || query[j] == '.' || query[j] == '"') {
				if query[j] == '"' {
					j = skipQuotedIdent(query, j)
					continue
				}
				j++
			}
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

// tableName returns tok as a table name — quotes removed, schema kept — or ""
// when it is not a plain (optionally qualified) identifier.
func tableName(tok string) string {
	name := strings.ReplaceAll(tok, `"`, "")
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
	// postgres block comments nest.
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

// skipString returns the index just past the string literal opening at i.
// A doubled quote is an escaped quote; with backslashes (an E-string) so is
// a backslash-quote.
func skipString(q string, i int, backslashes bool) int {
	i++ // opening quote
	for i < len(q) {
		switch {
		case backslashes && q[i] == '\\':
			i += 2
		case q[i] == '\'':
			if i+1 < len(q) && q[i+1] == '\'' {
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

func skipQuotedIdent(q string, i int) int {
	i++
	for i < len(q) {
		if q[i] == '"' {
			if i+1 < len(q) && q[i+1] == '"' {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(q)
}

// dollarQuoteEnd reports the index past a $tag$…$tag$ literal opening at i.
func dollarQuoteEnd(q string, i int) (int, bool) {
	j := i + 1
	if j < len(q) && isDigit(q[j]) {
		return 0, false // $1 is a placeholder
	}
	for j < len(q) && (isIdentStart(q[j]) || isDigit(q[j])) { // a tag has no $
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

func skipNumber(q string, i int) int {
	for i < len(q) && (isDigit(q[i]) || q[i] == '.' || q[i] == '_') {
		i++
	}
	if i < len(q) && (q[i] == 'e' || q[i] == 'E') {
		j := i + 1
		if j < len(q) && (q[j] == '+' || q[j] == '-') {
			j++
		}
		if j < len(q) && isDigit(q[j]) {
			i = j
			for i < len(q) && isDigit(q[i]) {
				i++
			}
		}
	}
	return i
}

func identBefore(q string, i int) bool { return i > 0 && isIdentPart(q[i-1]) }

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || c >= 0x80 || unicode.IsLetter(rune(c)) }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) || c == '$' }
