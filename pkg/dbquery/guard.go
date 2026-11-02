package dbquery

import (
	"fmt"
	"strconv"
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
// It also refuses SET and RESET by name, and any identifier that names a
// function in [deniedFunctions]: the ones that change the session (set_config,
// which a read-only transaction allows and through which a query can raise its
// own work_mem or drop its statement_timeout), run SQL text the guard never saw
// (ts_stat, ts_rewrite, query_to_xml, dblink, crosstab), take locks that outlive
// the statement, or reach the server's administration. The name is refused
// wherever it appears, schema-qualified or quoted alike, since attribute
// notation calls a function without parentheses; and every Unicode-escaped
// identifier (U&"...", with or without UESCAPE) is refused, since its spelling
// can hide the name. See [deniedIdentifier].
//
// IT IS NOT THE SECURITY BOUNDARY. A WITH can still carry a data-modifying
// CTE and a SELECT can still call nextval(); the read-only transaction [Run]
// opens is what refuses them, and the role's privileges are what refuse
// everything else. A denylist over SQL can only ever be as complete as the day
// it was written; the client-side deadline in [Run] bounds a query whatever it
// managed to set. The guard is pure Go (no cgo, no parser dependency) so the
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
	if leader == "SET" || leader == "RESET" {
		return "", refused("session_state", fmt.Sprintf("a query cannot %s: it may not change session settings", leader))
	}
	if !allowedLeaders[leader] {
		return "", refused("not_read_only", fmt.Sprintf("only SELECT, WITH, TABLE, VALUES, SHOW and EXPLAIN statements are allowed; got %s", leader))
	}
	if err := deniedIdentifier(stmt); err != nil {
		return "", err
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

// MaxParams is the most $n parameters a statement may reference; Postgres'
// own limit on bind parameters.
const MaxParams = 65535

// ParamRefs returns the highest $n parameter the statement references, 0 when
// it has none. It reads tokens, not text: a $1 inside a string literal, a
// quoted identifier, a comment or a dollar-quoted body is not a reference.
// $0 and a number past [MaxParams] are refused, as Postgres refuses them.
func ParamRefs(sql string) (max int, err error) {
	stmts, err := lex(sql)
	if err != nil {
		return 0, err
	}
	for _, stmt := range stmts {
		for _, t := range stmt {
			if t.kind != tokParam {
				continue
			}
			n, convErr := strconv.Atoi(t.text)
			if convErr != nil || n < 1 || n > MaxParams {
				return 0, refused("bad_param", fmt.Sprintf("$%s is not a valid parameter: parameters are $1 to $%d", t.text, MaxParams))
			}
			if n > max {
				max = n
			}
		}
	}
	return max, nil
}

// deniedFunctions are the functions a query may not call, by lower-cased name;
// deniedPrefixes cover whole families. Each one changes the session, runs SQL
// text the guard never read, leaves something behind on the connection, or
// reaches the server's administration. Most of the last need privileges a
// read-only role does not hold; they are listed so the refusal is a message
// rather than depend on that.
var deniedFunctions = map[string]string{
	"set_config": "a query may not change session settings, so the limits set for it hold",

	// They execute the SQL text (or the cursor) they are given.
	"ts_stat":                    "it runs the SQL text it is given",
	"ts_rewrite":                 "it runs the SQL text it is given",
	"query_to_xml":               "it runs the SQL text it is given",
	"query_to_xmlschema":         "it runs the SQL text it is given",
	"query_to_xml_and_xmlschema": "it runs the SQL text it is given",
	"cursor_to_xml":              "it reads a cursor the session holds",
	"cursor_to_xmlschema":        "it reads a cursor the session holds",
	"crosstab":                   "it runs the SQL text it is given",
	"crosstab2":                  "it runs the SQL text it is given",
	"crosstab3":                  "it runs the SQL text it is given",
	"crosstab4":                  "it runs the SQL text it is given",
	"connectby":                  "it runs a query it builds from its arguments",
	// They scan whole tables, schemas or the database behind one call.
	"table_to_xml":                  "it scans the whole table",
	"table_to_xmlschema":            "it scans the whole table",
	"table_to_xml_and_xmlschema":    "it scans the whole table",
	"schema_to_xml":                 "it scans every table of a schema",
	"schema_to_xmlschema":           "it scans every table of a schema",
	"schema_to_xml_and_xmlschema":   "it scans every table of a schema",
	"database_to_xml":               "it scans every table of the database",
	"database_to_xmlschema":         "it scans every table of the database",
	"database_to_xml_and_xmlschema": "it scans every table of the database",

	"pg_notify":                      "a query may not notify the database's listeners",
	"pg_terminate_backend":           "a query may not signal other sessions",
	"pg_cancel_backend":              "a query may not signal other sessions",
	"pg_reload_conf":                 "a query may not reconfigure the server",
	"pg_rotate_logfile":              "a query may not administer the server",
	"pg_switch_wal":                  "a query may not administer the server",
	"pg_create_restore_point":        "a query may not administer the server",
	"pg_logical_emit_message":        "a query may not write to the WAL",
	"pg_promote":                     "a query may not administer the server",
	"pg_wal_replay_pause":            "a query may not administer the server",
	"pg_wal_replay_resume":           "a query may not administer the server",
	"pg_backup_start":                "a query may not administer the server",
	"pg_backup_stop":                 "a query may not administer the server",
	"pg_log_backend_memory_contexts": "a query may not administer the server",
	"pg_import_system_collations":    "a query may not change the catalog",
	"pg_export_snapshot":             "a query may not export its snapshot to other sessions",

	// Large objects: listed by name, since lo_ is a prefix a project's own
	// functions may use.
	"lo_import": "a query may not touch large objects", "lo_export": "a query may not touch large objects",
	"lo_unlink": "a query may not touch large objects", "lo_create": "a query may not touch large objects",
	"lo_creat": "a query may not touch large objects", "lo_put": "a query may not touch large objects",
	"lo_from_bytea": "a query may not touch large objects", "lo_truncate": "a query may not touch large objects",
	"lo_truncate64": "a query may not touch large objects", "lo_open": "a query may not touch large objects",
	"lowrite": "a query may not touch large objects",
}

var deniedPrefixes = []struct{ prefix, why string }{
	{"dblink", "it opens a connection of its own"},
	{"pg_advisory_", "a query may not take locks that outlive or block its statement"},
	{"pg_try_advisory_", "a query may not take locks that outlive or block its statement"},
	{"pg_stat_reset", "a query may not reset the server's statistics"},
	{"pg_replication_", "a query may not touch replication"},
	{"pg_create_logical_replication_slot", "a query may not touch replication"},
	{"pg_create_physical_replication_slot", "a query may not touch replication"},
	{"pg_copy_logical_replication_slot", "a query may not touch replication"},
	{"pg_copy_physical_replication_slot", "a query may not touch replication"},
	{"pg_drop_replication_slot", "a query may not touch replication"},
	{"pg_logical_slot_", "a query may not touch replication"},
}

// deniedFunction reports why a call to name (any case) is refused, or false.
func deniedFunction(name string) (string, bool) {
	name = strings.ToLower(name)
	if why, ok := deniedFunctions[name]; ok {
		return why, true
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(name, p.prefix) {
			return p.why, true
		}
	}
	return "", false
}

// deniedIdentifier refuses the first identifier in stmt that names a denied
// function, or that is spelled with Unicode escapes.
//
// A denied name is refused wherever it appears, not only before "(":
// Postgres's attribute notation calls a one-argument function with no
// parentheses at all ((x).ts_stat is ts_stat(x)), and refusing the name
// everywhere means not having to know every position a call can take. The
// cost is that a column or table that shares a denied name cannot be named in a
// query; none of the names is a plausible column.
//
// A U&"..." identifier is refused wherever it appears, with or without a
// UESCAPE clause: its spelling can hide the name it stands for
// (U&"set!005fconfig" UESCAPE '!' is set_config), and a query has no need for
// one. It is the only syntax in which an identifier's spelling and its name
// differ by more than case and the "" escape, both of which the lexer undoes.
func deniedIdentifier(stmt []token) error {
	for _, t := range stmt {
		switch t.kind {
		case tokUnicodeIdent:
			return refused("unicode_identifier", "a Unicode-escaped identifier (U&\"...\") is not allowed in a query")
		case tokWord, tokQuotedIdent:
			if why, bad := deniedFunction(t.text); bad {
				return refused("denied_function", fmt.Sprintf("%s() is not allowed in a query: %s", strings.ToLower(t.text), why))
			}
		}
	}
	return nil
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
	tokLiteral      // string or dollar-quoted body
	tokQuotedIdent  // "name"; text is the name, "" unescaped
	tokUnicodeIdent // U&"name": its spelling can hide the name, so it is not read
	tokParam        // $n; text is the digits
)

type token struct {
	kind tokenKind
	text string // upper-cased for words, the name for a quoted identifier, "(" or ")" for punctuation
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
			end, escapes, err := scanString(sql, i)
			if err != nil {
				return nil, err
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
			kind := tokQuotedIdent
			// U&"..." spells the name with escapes (U&"set\005fconfig").
			if i >= 2 && sql[i-1] == '&' && (sql[i-2] == 'U' || sql[i-2] == 'u') && (i < 3 || !isIdentByte(sql[i-3])) {
				kind = tokUnicodeIdent
				if len(cur) >= 2 && cur[len(cur)-1].text == "&" && cur[len(cur)-2].text == "U" {
					cur = cur[:len(cur)-2]
				}
			}
			cur = append(cur, token{kind: kind, text: strings.ReplaceAll(sql[i+1:end-1], `""`, `"`)})
			i = end
		case c == '$' && (i == 0 || !isIdentByte(sql[i-1])):
			tag, ok := dollarTag(sql, i)
			if !ok {
				j := i + 1
				for j < n && sql[j] >= '0' && sql[j] <= '9' {
					j++
				}
				if j > i+1 {
					cur = append(cur, token{kind: tokParam, text: sql[i+1 : j]})
					i = j
				} else {
					i++
				}
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
		case isIdentByte(c):
			j := i
			for j < n && isIdentByte(sql[j]) {
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

// isIdentByte reports whether c can continue an identifier, as Postgres reads
// one: a letter, a digit, '_', '$', or any byte of a multi-byte character.
// The last matters at a boundary: in é$$ the $$ continues the identifier and
// opens no dollar quote, and in é'…' the literal is typed, not an E-string.
func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// scanString reads the string literal whose opening quote is sql[i], and its
// continuations, and returns the index after it and whether it is an E-string.
//
// E'...' takes backslash escapes; name'...' is a typed literal (the type
// "name" and a standard string), which is why the byte before the E must not
// continue an identifier. A literal continued on a following line is one
// literal, and the continuation of an E-string is read with escapes too:
// E'a'<newline>'\” is one string, not two.
func scanString(sql string, i int) (end int, escapes bool, err error) {
	escapes = i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isIdentByte(sql[i-2]))
	end, ok := scanQuoted(sql, i, '\'', escapes)
	for ok && escapes {
		next, more := continuation(sql, end)
		if !more {
			break
		}
		end, ok = scanQuoted(sql, next, '\'', true)
	}
	if !ok {
		return 0, false, refused("unterminated", "unterminated string literal")
	}
	return end, escapes, nil
}

// continuation returns the index of the quote that continues the string
// literal ending at sql[i], when there is one: Postgres joins two literals
// separated only by whitespace that includes a newline (a -- comment counts as
// whitespace). It is the scanner's quotecontinue rule:
//
//	horiz_whitespace* newline special_whitespace* quote
func continuation(sql string, i int) (int, bool) {
	n := len(sql)
	comment := func(j int) int { // past a -- comment, up to (not over) its newline
		for j < n && sql[j] != '\n' && sql[j] != '\r' {
			j++
		}
		return j
	}
	j := i
	for j < n {
		switch {
		case sql[j] == ' ' || sql[j] == '\t' || sql[j] == '\f':
			j++
			continue
		case sql[j] == '-' && j+1 < n && sql[j+1] == '-':
			j = comment(j)
			continue
		}
		break
	}
	if j >= n || (sql[j] != '\n' && sql[j] != '\r') {
		return 0, false
	}
	j++
	for j < n {
		switch c := sql[j]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			j++
			continue
		case c == '-' && j+1 < n && sql[j+1] == '-':
			// special_whitespace takes a comment only with its newline.
			if k := comment(j); k < n {
				j = k + 1
				continue
			}
		}
		break
	}
	if j < n && sql[j] == '\'' {
		return j, true
	}
	return 0, false
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
