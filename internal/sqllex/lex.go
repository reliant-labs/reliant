// Package sqllex splits SQL text into the statements postgres would run, the
// way postgres's lexer and psql's statement splitter read it. It is shared by
// the migration-safety lint (internal/linter/migrationlint), which reads a
// statement's words, and `forge db analyze` (pkg/dbanalyze), which executes
// each statement on its own and so needs its exact text.
//
// It imports nothing but the standard library.
package sqllex

import "strings"

// Statement is one top-level statement of a SQL file, read the way postgres
// reads it.
type Statement struct {
	// Line is the 1-based line of the statement's first token.
	Line int
	// Start and End are the byte offsets of the statement in the source:
	// src[Start:End] is its text, from its first token up to (not including)
	// the `;` that ends it, or the end of the source for an unterminated
	// last statement. A comment before the first token is not part of it.
	Start, End int
	// Words is every keyword or identifier in the statement, in order and
	// lowercased, at any parenthesis depth but never inside a comment, a
	// string or a dollar-quoted body. A quoted identifier keeps its quotes,
	// so `"vacuum"` can never read as the keyword.
	Words []string
	// Strings is every string constant ('…', E'…') in the statement, without
	// its quotes and with a doubled quote read as one.
	Strings []string
}

// Split splits a SQL file into its statements.
//
// The rules (unlike splitStatements' bare `;` split, which the older rules
// use) follow postgres's lexer and psql's statement splitter: a `;` ends a
// statement only outside a string constant ('…', E'…'), a quoted identifier,
// a dollar-quoted body ($$…$$, $tag$…$tag$), a line or (nested) block
// comment, parentheses, and — psql's rule for CREATE [OR REPLACE]
// FUNCTION|PROCEDURE — a BEGIN … END body (BEGIN ATOMIC). A file whose last
// statement is unterminated still counts it: postgres runs it.
//
// Counting statements right is the point. A rule that asks "is this the only
// statement in its file" must not see two where postgres sees one (a `;`
// inside a WHERE clause's string), nor one where postgres sees two.
func Split(src string) []Statement {
	var (
		out          []Statement
		cur          *Statement
		line         = 1
		paren, begin int
	)
	open := func(at int) {
		if cur == nil {
			cur = &Statement{Line: line, Start: at}
		}
	}
	// skip moves past src[i:j], counting the newlines it crosses.
	skip := func(i, j int) int {
		line += strings.Count(src[i:j], "\n")
		return j
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(src)
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skip(i, BlockCommentEnd(src, i))
		case c == '\'' || ((c == 'E' || c == 'e') && i+1 < len(src) && src[i+1] == '\'' && !IdentBefore(src, i)):
			open(i)
			q, escapes := i, c != '\''
			if escapes {
				q = i + 1
			}
			j := QuotedEnd(src, q, '\'', escapes)
			cur.Strings = append(cur.Strings, unquoteString(src[q:j], escapes))
			i = skip(i, j)
		case c == '"':
			open(i)
			j := QuotedEnd(src, i, '"', false)
			cur.Words = append(cur.Words, src[i:j])
			i = skip(i, j)
		case c == '$' && !IdentBefore(src, i):
			open(i)
			i = skip(i, DollarQuoteEnd(src, i))
		case IsIdentStart(c):
			open(i)
			j := i + 1
			for j < len(src) && IsIdentByte(src[j]) {
				j++
			}
			cur.Words = append(cur.Words, strings.ToLower(src[i:j]))
			if paren == 0 {
				begin = routineBodyDepth(cur.Words, begin)
			}
			i = j
		case c == '(':
			open(i)
			paren++
			i++
		case c == ')':
			open(i)
			if paren > 0 {
				paren--
			}
			i++
		case c == ';':
			i++
			if cur == nil || paren > 0 || begin > 0 {
				// An empty statement, or a `;` inside a body: not an end.
				continue
			}
			cur.End = i - 1
			out = append(out, *cur)
			cur, paren, begin = nil, 0, 0
		default:
			open(i)
			i++
		}
	}
	if cur != nil {
		cur.End = len(src)
		out = append(out, *cur)
	}
	return out
}

// routineBodyDepth is psql's BEGIN … END tracking: given a statement's words
// so far, the last one just read at the top level, it returns the depth of
// BEGIN … END the statement is in. Only a CREATE [OR REPLACE]
// FUNCTION|PROCEDURE has such a body (BEGIN ATOMIC); a CASE closes with END
// too, so it counts once inside one.
func routineBodyDepth(words []string, depth int) int {
	if !routineHeader(words) {
		return depth
	}
	switch words[len(words)-1] {
	case "begin":
		return depth + 1
	case "case":
		if depth > 0 {
			return depth + 1
		}
	case "end":
		if depth > 0 {
			return depth - 1
		}
	}
	return depth
}

// routineHeader reports whether a statement opens with CREATE [OR REPLACE]
// FUNCTION|PROCEDURE — psql's test for a body (BEGIN ATOMIC … END) whose
// semicolons do not end the statement.
func routineHeader(words []string) bool {
	switch {
	case len(words) >= 2 && words[0] == "create" && (words[1] == "function" || words[1] == "procedure"):
		return true
	case len(words) >= 4 && words[0] == "create" && words[1] == "or" && words[2] == "replace":
		return words[3] == "function" || words[3] == "procedure"
	}
	return false
}

// BlockCommentEnd returns the index just past the block comment opening at i.
// Postgres block comments nest.
func BlockCommentEnd(s string, i int) int {
	depth := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(s[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}

// QuotedEnd returns the index just past the quoted run opening at i, where a
// doubled quote is an escaped quote and, when backslashes is set (an E'…'
// string), so is a backslash-escaped byte.
func QuotedEnd(s string, i int, q byte, backslashes bool) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if backslashes {
				j++
			}
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(s)
}

// unquoteString returns a string constant's value: quotes stripped, a doubled
// quote read as one, and — in an E'…' string — a backslash-escaped quote too.
func unquoteString(quoted string, escapes bool) string {
	s := strings.TrimPrefix(quoted, "'")
	s = strings.TrimSuffix(s, "'")
	s = strings.ReplaceAll(s, "''", "'")
	if escapes {
		s = strings.ReplaceAll(s, `\'`, "'")
	}
	return s
}

// DollarQuoteEnd returns the index just past the dollar-quoted string opening
// at i, or i+1 when the `$` opens none (a `$1` parameter).
func DollarQuoteEnd(s string, i int) int {
	tag, ok := dollarTag(s, i)
	if !ok {
		return i + 1
	}
	if k := strings.Index(s[i+len(tag):], tag); k >= 0 {
		return i + len(tag) + k + len(tag)
	}
	return len(s)
}

// dollarTag returns the opening delimiter ($$ or $tag$) of a dollar-quoted
// string at i. A tag follows identifier rules but cannot contain `$` or start
// with a digit, which is what keeps a `$1` parameter from reading as one.
func dollarTag(s string, i int) (string, bool) {
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		switch {
		case c == '$':
			return s[i : j+1], true
		case c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z'):
		case c >= '0' && c <= '9' && j > i+1:
		default:
			return "", false
		}
	}
	return "", false
}

// IsIdentStart reports whether c can start an unquoted identifier.
func IsIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

// IsIdentByte reports whether c can continue an unquoted identifier.
func IsIdentByte(c byte) bool {
	return IsIdentStart(c) || c == '$' || (c >= '0' && c <= '9')
}

// IdentBefore reports whether s[i] continues an identifier: a `$` or a quote
// right after one belongs to it, so it never opens a dollar quote or an E'…'
// string.
func IdentBefore(s string, i int) bool {
	return i > 0 && IsIdentByte(s[i-1])
}
