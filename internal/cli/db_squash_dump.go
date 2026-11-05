// File: internal/cli/db_squash_dump.go
//
// What `forge db squash` keeps of pg_dump's output.
//
// pg_dump writes a script for psql to run on a fresh session, not a
// migration. It opens with a session prelude — ten SET statements, one of
// them `SELECT pg_catalog.set_config('search_path', '', false)` — and, from
// 16.10 and its sibling minor releases, brackets the script in
// `\restrict <key>` / `\unrestrict <key>` psql meta-commands.
//
// A migrator runs every migration of a run on ONE session, so that prelude
// does not end with the baseline:
//
//   - the empty search_path sticks, and the next migration in the same run
//     fails on its first unqualified name with `relation "x" does not exist`
//     (pg_dump qualifies every name it writes; hand-written migrations do not);
//   - AutoMigrate migrates on a connection borrowed from the app's pool and
//     hands it back still carrying the empty search_path;
//   - `\restrict` is not SQL at all, so golang-migrate fails on it.
//
// So the baseline keeps every statement that builds the database and drops
// every statement that configures the session running it. Of the privileges
// it keeps only the dashboard role's grants and PUBLIC's (privilegeStatementRe). A line-oriented
// filter (`grep -v '^SET '`) cannot do that safely: a function body or an
// inserted text value can hold a line that begins with `SET ` or `\`, and
// dropping it corrupts the body or the row. squashBaseline reads the dump the
// way postgres lexes it, so a line inside a quoted string, a quoted
// identifier, a dollar-quoted body or a comment is never mistaken for a
// statement or a meta-command.

package cli

import (
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/pkg/dbgrants"
)

// sessionStatementRe matches a top-level statement that configures the
// session rather than the database: every SET (pg_dump also emits
// `SET default_tablespace` / `SET default_table_access_method` between
// objects) and the set_config call pg_dump uses to empty the search_path.
// setval — the sequence state that goes with inserted rows — is a different
// function and is kept.
var sessionStatementRe = regexp.MustCompile(`(?is)^(?:SET|RESET)\b|^SELECT\s+(?:pg_catalog\s*\.\s*)?set_config\s*\(`)

// privilegeStatementRe matches a statement that grants or revokes. A squash
// dumps privileges so that two kinds survive it: the dashboard role's grants,
// which the project wrote as migrations (pkg/dbgrants, dashboard.go), and
// PUBLIC's, which the project changes with migrations too (`REVOKE EXECUTE ON
// FUNCTION f() FROM PUBLIC`, `REVOKE CREATE ON SCHEMA public FROM PUBLIC`). A
// baseline without them would build a database the role cannot read, or one
// where every role regains what a migration took away. Every other privilege
// is dropped, as `--no-privileges` dropped them all before: forge_ro's grants
// are re-converged after every run, and the rest name roles (the dumping
// user, the application's own) that are this database's, not the history's.
var privilegeStatementRe = regexp.MustCompile(`(?is)^(?:GRANT|REVOKE|ALTER\s+DEFAULT\s+PRIVILEGES)\b`)

// keptPrivilegeRe matches the privilege statements a baseline keeps: a GRANT
// to the dashboard role, and a GRANT to or REVOKE from PUBLIC. pg_dump writes
// one statement per grantee.
var keptPrivilegeRe = regexp.MustCompile(`(?is)^(?:GRANT\b.*\bTO\s+(?:"?` + regexp.QuoteMeta(dbgrants.DashboardRoleName) + `"?|PUBLIC)|REVOKE\b.*\bFROM\s+PUBLIC)\s*;$`)

// routineStatementRe matches a statement whose body postgres validates at
// CREATE time when check_function_bodies is on.
var routineStatementRe = regexp.MustCompile(`(?is)^CREATE\s+(?:OR\s+REPLACE\s+)?(?:FUNCTION|PROCEDURE)\b`)

// excessBlankLines matches more than two consecutive blank lines — the gaps
// the dropped prelude statements leave behind.
var excessBlankLines = regexp.MustCompile(`\n(?:[ \t]*\n){3,}`)

// checkFunctionBodiesPreamble is the one prelude setting a baseline still
// needs, re-stated so it cannot outlive the migration.
//
// pg_dump orders functions before tables, and it cannot see the tables a
// LANGUAGE sql body names, so it restores with check_function_bodies off.
// Dropped with the rest of the prelude, the first such function fails with
// `relation "public.x" does not exist` against a table created further down.
// set_config's third argument makes it transaction-local: golang-migrate runs
// a migration file as one multi-statement query, which postgres runs as one
// implicit transaction, so the setting ends with the baseline instead of
// following the session into the next migration or back into a pool.
const checkFunctionBodiesPreamble = `-- forge db squash: pg_dump writes functions before the tables their bodies may
-- name, so bodies are not validated while this migration runs. is_local = true
-- scopes that to this migration's own transaction; the next one validates.
SELECT pg_catalog.set_config('check_function_bodies', 'false', true);

`

// squashBaseline turns pg_dump's plain-format output into the body of a
// migration: every statement that builds the database, byte for byte, and
// nothing that configures the session or is addressed to psql. See the file
// comment for why each dropped piece breaks a migrator.
func squashBaseline(dump string) string {
	var (
		out      strings.Builder
		gap      strings.Builder // whitespace and comments since the last kept statement
		stmt     strings.Builder // the statement being read
		inStmt   bool
		routines bool
	)
	flushGap := func() {
		out.WriteString(excessBlankLines.ReplaceAllString(gap.String(), "\n\n\n"))
		gap.Reset()
	}

	s := dump
	for i := 0; i < len(s); {
		dst := &gap
		if inStmt {
			dst = &stmt
		}
		c := s[i]
		switch {
		case c == '\\' && (i == 0 || s[i-1] == '\n'):
			// A psql meta-command (\restrict, \unrestrict, \connect, …) runs
			// to the end of its line. Inside a quote or body this branch is
			// never reached: those are consumed whole by sqlTokenEnd.
			i = lineEnd(s, i)
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := lineEnd(s, i)
			dst.WriteString(s[i:j])
			i = j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := blockCommentEnd(s, i)
			dst.WriteString(s[i:j])
			i = j
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			dst.WriteByte(c)
			i++
		case c == ';':
			stmt.WriteByte(c)
			i++
			text := stmt.String()
			stmt.Reset()
			inStmt = false
			if sessionStatementRe.MatchString(text) ||
				privilegeStatementRe.MatchString(text) && !keptPrivilegeRe.MatchString(text) {
				// Dropped. The gap before it stays pending, so the comments
				// around it survive and the blank lines it leaves collapse.
				continue
			}
			if routineStatementRe.MatchString(text) {
				routines = true
			}
			flushGap()
			out.WriteString(text)
		default:
			if !inStmt {
				inStmt = true
				dst = &stmt
			}
			j := sqlTokenEnd(s, i)
			dst.WriteString(s[i:j])
			i = j
		}
	}
	flushGap()
	// pg_dump terminates every statement; an unterminated tail is kept
	// rather than silently dropped, so a malformed dump fails when it runs.
	out.WriteString(stmt.String())

	if routines {
		return checkFunctionBodiesPreamble + out.String()
	}
	return out.String()
}

// lineEnd returns the index just past the newline ending the line at i, or
// len(s).
func lineEnd(s string, i int) int {
	if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// blockCommentEnd returns the index just past the block comment opening at i.
// Postgres block comments nest.
func blockCommentEnd(s string, i int) int {
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

// sqlTokenEnd returns the index just past the token starting at i when it is
// one that can hold arbitrary text — a string constant ('…', E'…'), a quoted
// identifier ("…") or a dollar-quoted string ($tag$…$tag$) — and i+1 for any
// other byte.
func sqlTokenEnd(s string, i int) int {
	switch c := s[i]; {
	case c == '\'':
		return quotedEnd(s, i, '\'', false)
	case c == '"':
		return quotedEnd(s, i, '"', false)
	case (c == 'E' || c == 'e') && i+1 < len(s) && s[i+1] == '\'' && (i == 0 || !isSQLIdentByte(s[i-1])):
		// An escape string constant: a backslash escapes the next byte,
		// including a quote.
		return quotedEnd(s, i+1, '\'', true)
	case c == '$' && (i == 0 || !isSQLIdentByte(s[i-1])):
		if tag, ok := dollarTag(s, i); ok {
			if j := strings.Index(s[i+len(tag):], tag); j >= 0 {
				return i + len(tag) + j + len(tag)
			}
			return len(s)
		}
	}
	return i + 1
}

// quotedEnd returns the index just past the quoted run opening at i, where a
// doubled quote is an escaped quote and, when backslashes is set, so is a
// backslash-quote.
func quotedEnd(s string, i int, q byte, backslashes bool) int {
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

// dollarTag returns the opening delimiter ($$ or $tag$) of a dollar-quoted
// string at i. A tag follows identifier rules but cannot contain `$` or start
// with a digit, which is what keeps a `$1` parameter from being read as one.
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

// isSQLIdentByte reports whether c can continue an identifier. A `$` or a
// quote right after one belongs to the identifier (or is a syntax error), so
// it never opens a dollar quote or an escape string.
func isSQLIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'z')
}
