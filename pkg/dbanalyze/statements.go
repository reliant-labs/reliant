package dbanalyze

import (
	"regexp"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/sqllex"
)

// Statement is one top-level statement of a SQL file, split the way postgres
// reads it (comments, string constants, dollar-quoted bodies and BEGIN ATOMIC
// bodies never end a statement).
type Statement struct {
	// Index is the statement's 0-based position in its file.
	Index int
	// Line is the 1-based line of its first token.
	Line int
	// SQL is its text, without the terminating `;`.
	SQL   string
	words []string
}

// SplitStatements splits a SQL file into its statements.
func SplitStatements(src string) []Statement {
	lexed := sqllex.Split(src)
	out := make([]Statement, 0, len(lexed))
	for i, l := range lexed {
		out = append(out, Statement{
			Index: i,
			Line:  l.Line,
			SQL:   strings.TrimSpace(src[l.Start:l.End]),
			words: l.Words,
		})
	}
	return out
}

// NonTransactional names the statement when postgres refuses it inside a
// transaction block (CREATE INDEX CONCURRENTLY, VACUUM, …), else "". Such a
// statement cannot run in an analysis session.
func (st Statement) NonTransactional() string { return sqllex.NonTransactional(st.words) }

// NonConcurrently returns a CREATE [UNIQUE] INDEX CONCURRENTLY statement
// without CONCURRENTLY, which can run inside a transaction: a pass that needs
// the index to exist inside one (a query plan that should use it) builds it
// this way. The migration pass does not: it runs the concurrent statement
// itself, on a disposable database. False for any other statement.
func (st Statement) NonConcurrently() (string, bool) {
	loc := concurrentIndexRE.FindStringSubmatchIndex(st.SQL)
	if loc == nil {
		return "", false
	}
	return st.SQL[:loc[3]] + st.SQL[loc[1]:], true
}

var concurrentIndexRE = regexp.MustCompile(`(?is)^(\s*create\s+(?:unique\s+)?index\s+)concurrently\s+`)

// transactionControl names a statement that opens, ends or subdivides a
// transaction (BEGIN, COMMIT, SAVEPOINT, …), else "".
func (st Statement) transactionControl() string {
	switch w := st.first(); w {
	case "begin", "start", "commit", "end", "rollback", "abort", "savepoint", "release":
		return strings.ToUpper(w)
	case "prepare":
		if len(st.words) > 1 && st.words[1] == "transaction" {
			return "PREPARE TRANSACTION"
		}
	}
	return ""
}

// serverWide names what a statement changes that belongs to the whole
// server rather than to the database it runs in — roles, databases,
// tablespaces, the server's configuration, a subscription's replication
// slot — else "". A copy of the database does not isolate such a change, so
// the analysis does not run the statement. (A DO block or a function can
// make the same changes out of sight; that is not detected.)
func (st Statement) serverWide() string {
	w := st.words
	at := func(i int, word string) bool { return len(w) > i && w[i] == word }
	switch {
	case (at(0, "create") || at(0, "alter") || at(0, "drop")) &&
		(at(1, "role") || at(1, "group") || at(1, "user") && !at(2, "mapping")):
		return "a role"
	case (at(0, "create") || at(0, "alter") || at(0, "drop")) && at(1, "database"):
		return "a database"
	case (at(0, "create") || at(0, "alter") || at(0, "drop")) && at(1, "tablespace"):
		return "a tablespace"
	case (at(0, "create") || at(0, "alter") || at(0, "drop")) && at(1, "subscription"):
		return "a subscription"
	case at(0, "alter") && at(1, "system"):
		return "the server's configuration (ALTER SYSTEM)"
	case at(0, "comment") && at(1, "on") && (at(2, "database") || at(2, "role") || at(2, "tablespace")),
		at(0, "security") && at(1, "label") && (st.has("database") || st.has("role") || st.has("tablespace")):
		return "a database, role or tablespace's label"
	case at(0, "reassign") && at(1, "owned"), at(0, "drop") && at(1, "owned"):
		return "a role's ownership and privileges"
	case at(0, "grant") || at(0, "revoke"):
		i := slices.Index(w, "on")
		if i < 0 {
			return "a role's membership"
		}
		if at(i+1, "database") || at(i+1, "tablespace") || at(i+1, "parameter") {
			return "a privilege on a " + w[i+1]
		}
	}
	return ""
}

// nonBlocking reports whether a statement that cannot run in a transaction is
// one that neither rewrites a table nor blocks writes to one: the
// CONCURRENTLY forms, which take SHARE UPDATE EXCLUSIVE, plain VACUUM (also
// SHARE UPDATE EXCLUSIVE, no rewrite), and DISCARD ALL. VACUUM FULL, CLUSTER
// and a non-concurrent REINDEX of a schema or database are not.
func (st Statement) nonBlocking() bool {
	switch st.NonTransactional() {
	case "CREATE INDEX CONCURRENTLY", "DROP INDEX CONCURRENTLY", "REINDEX … CONCURRENTLY",
		"ALTER TABLE … DETACH PARTITION … CONCURRENTLY", "DISCARD ALL":
		return true
	case "VACUUM":
		return !st.optionOn("full")
	}
	return false
}

// optionOn reports whether a statement names option and does not turn it
// off: VACUUM FULL, VACUUM (FULL), VACUUM (FULL true), but not
// VACUUM (FULL false).
func (st Statement) optionOn(option string) bool {
	i := slices.Index(st.words, option)
	if i < 0 {
		return false
	}
	if i+1 < len(st.words) {
		switch st.words[i+1] {
		case "false", "off", "0":
			return false
		}
	}
	return true
}

// first is the statement's leading keyword, lowercased.
func (st Statement) first() string {
	if len(st.words) == 0 {
		return ""
	}
	return st.words[0]
}

// has reports whether the statement holds every one of words.
func (st Statement) has(words ...string) bool {
	for _, w := range words {
		found := false
		for _, sw := range st.words {
			if sw == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// staticLocks is the lock a statement that was not executed is known to
// take, from its kind alone: the concurrent index statements take SHARE
// UPDATE EXCLUSIVE, which blocks DDL and VACUUM but neither reads nor
// writes. Other non-transactional statements report none.
func (st Statement) staticLocks() []Lock {
	const mode = "ShareUpdateExclusiveLock"
	switch {
	case st.first() == "create" && st.has("index", "concurrently"):
		if m := indexTableRE.FindStringSubmatch(st.SQL); m != nil {
			return []Lock{{Relation: unquoteName(m[1]), Mode: mode, Static: true}}
		}
	case st.first() == "drop" && st.has("index", "concurrently"),
		st.first() == "reindex" && st.has("concurrently"):
		if m := lastNameRE.FindStringSubmatch(st.SQL); m != nil {
			return []Lock{{Relation: unquoteName(m[1]), Mode: mode, Static: true}}
		}
	}
	return nil
}

const sqlName = `(?:"[^"]+"|[\w$]+)(?:\.(?:"[^"]+"|[\w$]+))?`

var (
	indexTableRE = regexp.MustCompile(`(?is)\bon\s+(?:only\s+)?(` + sqlName + `)`)
	lastNameRE   = regexp.MustCompile(`(?is)(` + sqlName + `)\s*$`)
)

// unquoteName renders a SQL name the way the document names relations:
// unquoted parts folded to lower case as postgres folds them, quotes
// removed, public's schema dropped.
func unquoteName(name string) string {
	parts := sqlNamePartRE.FindAllString(name, -1)
	for i, p := range parts {
		if strings.HasPrefix(p, `"`) {
			parts[i] = strings.Trim(p, `"`)
		} else {
			parts[i] = strings.ToLower(p)
		}
	}
	if len(parts) == 2 && parts[0] == "public" {
		parts = parts[1:]
	}
	return strings.Join(parts, ".")
}

var sqlNamePartRE = regexp.MustCompile(`"[^"]+"|[\w$]+`)
