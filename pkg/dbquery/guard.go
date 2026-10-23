package dbquery

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxSQLBytes caps the statement text.
const MaxSQLBytes = 16 << 10

// allowedLeaders are the statements a query may start with.
var allowedLeaders = map[string]bool{
	"SELECT": true, "WITH": true, "TABLE": true, "VALUES": true, "SHOW": true, "EXPLAIN": true,
}

// explainable are the statements EXPLAIN may describe.
var explainable = map[string]bool{"SELECT": true, "WITH": true, "TABLE": true, "VALUES": true}

// Check is the lexical guard: the first line of defence, and the source of
// good error messages. It accepts exactly one statement that starts with
// SELECT, WITH, TABLE, VALUES, SHOW or EXPLAIN, and refuses EXPLAIN ANALYZE
// over anything but a SELECT, because ANALYZE executes what it explains.
//
// IT IS NOT THE SECURITY BOUNDARY. A WITH can still carry a data-modifying
// CTE and a SELECT can still call nextval(); the read-only transaction [Run]
// opens is what refuses them, and the role's privileges are what refuse
// everything else. The guard is pure Go (no cgo, no parser dependency) so the
// forge binary stays cross-compilable; it understands comments, quoted
// identifiers, string literals (including E” and dollar quoting) well enough
// to find the statement's end, and no more.
//
// On success it returns the statement's leading keyword, upper-cased.
func Check(sql string) (leader string, err error) {
	if len(sql) > MaxSQLBytes {
		return "", refused("sql_too_large", fmt.Sprintf("the statement is %d bytes; the limit is %d", len(sql), MaxSQLBytes))
	}
	if !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) {
		return "", refused("invalid_text", "the statement is not valid UTF-8 text")
	}
	toks, err := lex(sql)
	if err != nil {
		return "", err
	}
	var stmt []token
	for _, s := range toks {
		switch {
		case len(s) == 0:
		case stmt != nil:
			return "", refused("multiple_statements", "only one statement per query; found a second after ';'")
		default:
			stmt = s
		}
	}
	if stmt == nil {
		return "", refused("empty", "the statement is empty")
	}

	i := skipParens(stmt, 0)
	if i >= len(stmt) || stmt[i].kind != tokWord {
		return "", refused("not_read_only", "the statement must start with SELECT, WITH, TABLE, VALUES, SHOW or EXPLAIN")
	}
	leader = stmt[i].text
	if !allowedLeaders[leader] {
		return "", refused("not_read_only", fmt.Sprintf("only SELECT, WITH, TABLE, VALUES, SHOW and EXPLAIN statements are allowed; got %s", leader))
	}
	if leader != "EXPLAIN" {
		return leader, nil
	}

	// EXPLAIN [ ( option [, ...] ) ] stmt   |   EXPLAIN [ANALYZE] [VERBOSE] stmt
	analyze := false
	i++
	if i < len(stmt) && stmt[i].kind == tokPunct && stmt[i].text == "(" {
		depth := 0
		for ; i < len(stmt); i++ {
			t := stmt[i]
			switch {
			case t.kind == tokPunct && t.text == "(":
				depth++
			case t.kind == tokPunct && t.text == ")":
				depth--
			case t.kind == tokWord && (t.text == "ANALYZE" || t.text == "ANALYSE"):
				analyze = true
			}
			if depth == 0 {
				i++
				break
			}
		}
	} else {
		for ; i < len(stmt) && stmt[i].kind == tokWord && (stmt[i].text == "ANALYZE" || stmt[i].text == "ANALYSE" || stmt[i].text == "VERBOSE"); i++ {
			if stmt[i].text != "VERBOSE" {
				analyze = true
			}
		}
	}
	i = skipParens(stmt, i)
	if i >= len(stmt) || stmt[i].kind != tokWord {
		return "", refused("not_read_only", "EXPLAIN needs a SELECT, WITH, TABLE or VALUES statement to describe")
	}
	inner := stmt[i].text
	if !explainable[inner] {
		return "", refused("not_read_only", fmt.Sprintf("EXPLAIN may only describe SELECT, WITH, TABLE or VALUES; got %s", inner))
	}
	if analyze && inner != "SELECT" {
		return "", refused("explain_analyze", fmt.Sprintf("EXPLAIN ANALYZE runs the statement it describes; it is only allowed over a SELECT, got %s", inner))
	}
	return leader, nil
}

func skipParens(stmt []token, i int) int {
	for i < len(stmt) && stmt[i].kind == tokPunct && stmt[i].text == "(" {
		i++
	}
	return i
}

type tokenKind int

const (
	tokWord tokenKind = iota
	tokPunct
	tokLiteral // string, quoted identifier, dollar-quoted body
)

type token struct {
	kind tokenKind
	text string // upper-cased for words, "(" or ")" for punctuation
}

// lex splits sql into statements at top-level semicolons, dropping comments
// and whitespace. Each statement is its significant tokens; a statement with
// none (a trailing ';', a comment) is empty.
func lex(sql string) ([][]token, error) {
	var (
		stmts [][]token
		cur   []token
		i     int
		n     = len(sql)
	)
	flush := func() { stmts = append(stmts, cur); cur = nil }
	for i < n {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' && sql[i] != '\r' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			depth := 0
			for {
				switch {
				case i+1 < n && sql[i] == '/' && sql[i+1] == '*':
					depth++
					i += 2
				case i+1 < n && sql[i] == '*' && sql[i+1] == '/':
					depth--
					i += 2
				case i < n:
					i++
				default:
					return nil, refused("unterminated", "unterminated /* comment")
				}
				if depth == 0 {
					break
				}
			}
		case c == '\'':
			escapes := i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isIdentByte(sql[i-2]))
			end, ok := scanQuoted(sql, i, '\'', escapes)
			if !ok {
				return nil, refused("unterminated", "unterminated string literal")
			}
			// An E before the quote was lexed as a word; it is a prefix.
			if escapes && len(cur) > 0 && cur[len(cur)-1].kind == tokWord && cur[len(cur)-1].text == "E" {
				cur = cur[:len(cur)-1]
			}
			cur = append(cur, token{kind: tokLiteral})
			i = end
		case c == '"':
			end, ok := scanQuoted(sql, i, '"', false)
			if !ok {
				return nil, refused("unterminated", "unterminated quoted identifier")
			}
			cur = append(cur, token{kind: tokLiteral})
			i = end
		case c == '$' && (i == 0 || !isIdentByte(sql[i-1])):
			tag, ok := dollarTag(sql, i)
			if !ok {
				i++
				continue
			}
			end := strings.Index(sql[i+len(tag):], tag)
			if end < 0 {
				return nil, refused("unterminated", "unterminated dollar-quoted string")
			}
			cur = append(cur, token{kind: tokLiteral})
			i += len(tag) + end + len(tag)
		case c == ';':
			flush()
			i++
		case c == '(' || c == ')':
			cur = append(cur, token{kind: tokPunct, text: string(c)})
			i++
		case isIdentByte(c) || c >= 0x80:
			j := i
			for j < n && (isIdentByte(sql[j]) || sql[j] >= 0x80) {
				j++
			}
			cur = append(cur, token{kind: tokWord, text: strings.ToUpper(sql[i:j])})
			i = j
		default:
			cur = append(cur, token{kind: tokPunct, text: string(c)})
			i++
		}
	}
	flush()
	return stmts, nil
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// scanQuoted returns the index after the closing quote of the literal that
// opens at sql[start]. A doubled quote is an escaped quote; with backslash
// escapes (E”) a backslash escapes the next byte.
func scanQuoted(sql string, start int, quote byte, backslash bool) (int, bool) {
	for i := start + 1; i < len(sql); i++ {
		switch {
		case backslash && sql[i] == '\\':
			i++
		case sql[i] == quote:
			if i+1 < len(sql) && sql[i+1] == quote {
				i++
				continue
			}
			return i + 1, true
		}
	}
	return 0, false
}

// dollarTag returns "$tag$" when sql[i:] opens a dollar-quoted string. $1 is a
// parameter, not a quote: a tag may not start with a digit.
func dollarTag(sql string, i int) (string, bool) {
	j := i + 1
	for j < len(sql) && (sql[j] == '_' || (sql[j] >= 'a' && sql[j] <= 'z') || (sql[j] >= 'A' && sql[j] <= 'Z') || sql[j] >= 0x80 ||
		(j > i+1 && sql[j] >= '0' && sql[j] <= '9')) {
		j++
	}
	if j < len(sql) && sql[j] == '$' {
		return sql[i : j+1], true
	}
	return "", false
}
