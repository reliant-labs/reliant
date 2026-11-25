package migratekit

import (
	"strings"
)

// A MIGRATION IS RETRIED ONLY WHEN IT IS PROVEN ALL-OR-NOTHING.
//
// golang-migrate sends a file to postgres as one query, which postgres runs as
// one implicit transaction: a failure rolls the whole file back, and running it
// again is a retry. That stops being true the moment the file ends the
// transaction itself (`BEGIN; … COMMIT; ALTER TABLE …` commits the first half
// before the second can time out), and a retry would then repeat work that is
// already committed. Whether a file does that cannot be read off the text with
// a regular expression: `COMMIT` can follow a `;` on the same line, sit behind
// a comment, or appear inside a string or a function body, where it means
// nothing. So the file is tokenized the way postgres's lexer reads it —
// comments, string constants, quoted identifiers and dollar-quoted bodies are
// skipped — and its statements are classified from what is left.
//
// When the classification cannot prove a file retryable, it is not retried:
// the first lock timeout fails the run and leaves the schema dirty at the
// file's version, as any partly applied migration does.

type tokKind byte

const (
	tokWord   tokKind = 'w' // an unquoted identifier or keyword, lower-cased
	tokQuoted tokKind = 'q' // a "quoted identifier", exactly as written
	tokOther  tokKind = 'o' // a string constant or dollar-quoted body (content dropped)
	tokPunct  tokKind = 'p' // one punctuation character
)

type sqlTok struct {
	kind tokKind
	text string
}

func (t sqlTok) isWord(w string) bool { return t.kind == tokWord && t.text == w }

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentByte(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' || c == '$' }

// splitStatements tokenizes src and splits it at the `;` postgres would end a
// statement at: one outside comments, strings, quoted identifiers and
// dollar-quoted bodies. Empty statements are dropped.
func splitStatements(src string) [][]sqlTok {
	var (
		out [][]sqlTok
		cur []sqlTok
	)
	end := func() {
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ';':
			end()
			i++
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(src) && src[i+1] == '-':
			if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(src)
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = blockCommentEnd(src, i)
		case c == '\'':
			i = quotedEnd(src, i, '\'', false)
			cur = append(cur, sqlTok{kind: tokOther})
		case c == '"':
			j := quotedEnd(src, i, '"', false)
			cur = append(cur, sqlTok{kind: tokQuoted, text: strings.ReplaceAll(src[i+1:max(i+1, j-1)], `""`, `"`)})
			i = j
		case c == '$':
			if j, ok := dollarQuoteEnd(src, i); ok {
				cur = append(cur, sqlTok{kind: tokOther})
				i = j
			} else {
				cur = append(cur, sqlTok{kind: tokPunct, text: "$"})
				i++
			}
		case isIdentStart(c):
			j := i + 1
			for j < len(src) && isIdentByte(src[j]) {
				j++
			}
			word := src[i:j]
			if (word == "e" || word == "E") && j < len(src) && src[j] == '\'' {
				i = quotedEnd(src, j, '\'', true)
				cur = append(cur, sqlTok{kind: tokOther})
				continue
			}
			cur = append(cur, sqlTok{kind: tokWord, text: strings.ToLower(word)})
			i = j
		default:
			cur = append(cur, sqlTok{kind: tokPunct, text: string(c)})
			i++
		}
	}
	end()
	return out
}

// blockCommentEnd returns the index after the (nested) block comment starting
// at src[i:], or len(src) when it is unterminated.
func blockCommentEnd(src string, i int) int {
	depth := 0
	for i < len(src) {
		switch {
		case strings.HasPrefix(src[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(src[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(src)
}

// quotedEnd returns the index after the quoted run starting at src[i] (the
// opening quote), where a doubled quote is an escaped one and, when
// backslashes is set (an E'…' string), `\` escapes the next byte. An
// unterminated run extends to the end.
func quotedEnd(src string, i int, q byte, backslashes bool) int {
	for j := i + 1; j < len(src); j++ {
		switch {
		case backslashes && src[j] == '\\':
			j++
		case src[j] == q:
			if j+1 < len(src) && src[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(src)
}

// dollarQuoteEnd returns the index after the dollar-quoted body whose opening
// tag starts at src[i], and ok=false when src[i] does not open one (`$1`).
func dollarQuoteEnd(src string, i int) (int, bool) {
	if i > 0 && isIdentByte(src[i-1]) {
		return 0, false
	}
	j := i + 1
	if j < len(src) && isIdentStart(src[j]) {
		for j < len(src) && isIdentByte(src[j]) && src[j] != '$' {
			j++
		}
	}
	if j >= len(src) || src[j] != '$' {
		return 0, false
	}
	tag := src[i : j+1]
	if k := strings.Index(src[j+1:], tag); k >= 0 {
		return j + 1 + k + len(tag), true
	}
	return len(src), true
}

// A createIndexTarget is the index a CREATE INDEX CONCURRENTLY statement
// builds: its name, and the table it is on exactly as written (qualified or
// not), rendered so that to_regclass resolves it under postgres's own
// search-path rules. An index lives in its table's schema, so the table
// decides where the index is.
type createIndexTarget struct {
	index string
	table string
}

// filePlan is what a migration file is, from its statements.
type filePlan struct {
	// atomic is true only when every statement of the file is on the
	// allowlist below and none ends the transaction, runs code that may
	// commit, or runs outside a transaction block. Anything the scan cannot
	// place is not atomic: the default is "not proven".
	atomic bool
	// creates is the one CREATE INDEX CONCURRENTLY a file consisting of
	// nothing else builds. It is set only for such a lone statement: with
	// anything around it the build would run in a different session state
	// than the cleanup that looks for its leftover (a SET search_path in the
	// same file changes what its table name means), and a file that is not
	// proven all-or-nothing is never cleaned up behind.
	creates []createIndexTarget
}

// retryable reports whether a failed attempt at the file can be run again.
func (p filePlan) retryable() bool { return p.atomic }

// atomicStatements are the first words of statements that postgres runs in
// the file's implicit transaction and rolls back whole on failure. The list is
// deliberately an allowlist: a statement it does not name (BEGIN/COMMIT,
// CALL and DO, which may COMMIT inside, VACUUM, REINDEX, CLUSTER, ALTER SYSTEM,
// CREATE DATABASE …) makes the file unproven, so the first lock timeout fails
// the run and leaves the schema dirty for a human.
var atomicStatements = map[string]bool{
	"create": true, "alter": true, "drop": true, "insert": true, "update": true,
	"delete": true, "merge": true, "truncate": true, "comment": true,
	"grant": true, "revoke": true, "select": true, "with": true, "set": true,
	"reset": true, "lock": true, "analyze": true, "refresh": true,
	"security": true, "reassign": true,
}

// notAtomic reports whether a CREATE, ALTER or DROP statement is one of the
// few that cannot run in a transaction block or commits on its own.
func notAtomic(s []sqlTok) bool {
	if len(s) < 2 {
		return false
	}
	switch s[0].text {
	case "create", "drop":
		for _, w := range []string{"database", "tablespace", "subscription"} {
			if s[1].isWord(w) {
				return true
			}
		}
	case "alter":
		for _, w := range []string{"system", "database", "subscription"} {
			if s[1].isWord(w) {
				return true
			}
		}
		if s[1].isWord("type") {
			for i := 2; i+1 < len(s); i++ {
				if s[i].isWord("add") && s[i+1].isWord("value") {
					return true
				}
			}
		}
	}
	return false
}

func planFile(body []byte) filePlan {
	stmts := splitStatements(string(body))
	p := filePlan{atomic: len(stmts) > 0}
	for _, s := range stmts {
		first := s[0]
		concurrently := false
		for _, t := range s {
			if t.isWord("concurrently") {
				concurrently = true
				break
			}
		}
		switch {
		case first.kind != tokWord || !atomicStatements[first.text] || notAtomic(s):
			p.atomic = false
		case concurrently && createsIndex(s) && len(stmts) == 1:
			// A lone CREATE INDEX CONCURRENTLY runs outside a transaction
			// block; a timed-out build leaves only an INVALID index, which
			// the cleanup drops. DROP INDEX CONCURRENTLY is not this: it can
			// commit the index invalid before it times out waiting.
			if t, ok := createIndexOf(s); ok {
				p.creates = []createIndexTarget{t}
			} else {
				p.atomic = false
			}
		case concurrently:
			p.atomic = false
		}
	}
	if !p.atomic {
		p.creates = nil
	}
	return p
}

func createsIndex(s []sqlTok) bool {
	i := 1
	if len(s) > 1 && s[1].isWord("unique") {
		i = 2
	}
	return s[0].isWord("create") && len(s) > i && s[i].isWord("index")
}

// createIndexOf reads `CREATE [UNIQUE] INDEX CONCURRENTLY [IF NOT EXISTS]
// name ON [ONLY] [schema.]table …`.
func createIndexOf(s []sqlTok) (createIndexTarget, bool) {
	i := 2
	if s[1].isWord("unique") {
		i = 3
	}
	skip := func(words ...string) {
		for _, w := range words {
			if i < len(s) && s[i].isWord(w) {
				i++
			} else {
				return
			}
		}
	}
	skip("concurrently")
	if i+2 < len(s) && s[i].isWord("if") && s[i+1].isWord("not") && s[i+2].isWord("exists") {
		i += 3
	}
	if i >= len(s) || !identTok(s[i]) || s[i].isWord("on") {
		return createIndexTarget{}, false
	}
	name := s[i].text
	i++
	if i >= len(s) || !s[i].isWord("on") {
		return createIndexTarget{}, false
	}
	i++
	skip("only")
	var table []string
	for i < len(s) && identTok(s[i]) {
		table = append(table, quoteIdent(s[i].text))
		i++
		if i < len(s) && s[i].kind == tokPunct && s[i].text == "." {
			i++
			continue
		}
		break
	}
	if len(table) == 0 || len(table) > 2 {
		return createIndexTarget{}, false
	}
	return createIndexTarget{index: name, table: strings.Join(table, ".")}, true
}

func identTok(t sqlTok) bool { return t.kind == tokWord || t.kind == tokQuoted }
