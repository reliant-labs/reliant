package migrationlint

import (
	"regexp"
	"strconv"
	"strings"
)

// A valid CHECK (<column> IS NOT NULL) proves every row of the column
// non-null, and postgres then skips the scan a SET NOT NULL would otherwise
// make. So a SET NOT NULL over such a CHECK cannot fail on a populated table,
// and needs no UPDATE backfill. It is the path `forge db analyze`
// recommends for a large table: ADD CONSTRAINT … CHECK (c IS NOT NULL)
// NOT VALID, VALIDATE CONSTRAINT in a later migration, SET NOT NULL in a
// third. set-not-null-without-backfill reads one file at a time and could not
// see the first two, so it failed the third file of the advice it is paired
// with.
//
// dropCheckProvenSetNotNull is the cross-file pass that sees them: it walks
// the migrations in apply order, tracks which CHECK constraints are valid
// (added without NOT VALID, or validated since) and still present, and drops
// the set-not-null-without-backfill findings whose column such a constraint
// covers at that point. Like the per-file rule it reads only the SQL text,
// so the proof must sit in this directory's migrations.

var (
	// alterTableNameRe is an ALTER TABLE prefix through the table name and
	// PostgreSQL's optional inheritance star (`ALTER TABLE users * …`).
	alterTableNameRe = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?((?:"[^"]+"|[a-zA-Z_][\w$]*)(?:\s*\.\s*(?:"[^"]+"|[a-zA-Z_][\w$]*))?)(?:\s*\*)?`)
	// addNotNullCheckRe is a WHOLE clause: ADD CONSTRAINT <name> CHECK
	// (<col> IS NOT NULL), the expression optionally parenthesized once,
	// then an optional NOT VALID and nothing else. Anchored at both ends, so
	// an expression with another predicate (`(c IS NOT NULL) OR true`,
	// which admits NULLs) or a modifier this does not know proves nothing.
	addNotNullCheckRe    = regexp.MustCompile(`(?is)^\s*add\s+constraint\s+("[^"]+"|[a-zA-Z_][\w$]*)\s+check\s*\(\s*(?:\(\s*("[^"]+"|[a-zA-Z_][\w$]*)\s+is\s+not\s+null\s*\)|("[^"]+"|[a-zA-Z_][\w$]*)\s+is\s+not\s+null)\s*\)\s*(not\s+valid)?\s*$`)
	validateConstraintRe = regexp.MustCompile(`(?is)^\s*validate\s+constraint\s+("[^"]+"|[a-zA-Z_][\w$]*)\s*$`)
	dropConstraintRe     = regexp.MustCompile(`(?is)^\s*drop\s+constraint\s+(?:if\s+exists\s+)?("[^"]+"|[a-zA-Z_][\w$]*)`)
	setNotNullClauseRe   = regexp.MustCompile(`(?is)^\s*alter\s+(?:column\s+)?("[^"]+"|[a-zA-Z_][\w$]*)\s+set\s+not\s+null\s*$`)
	searchPathChangeRe   = regexp.MustCompile(`(?is)^\s*(?:(?:set\s+(?:(?:local|session)\s+)?|reset\s+)search_path\b|reset\s+all\b|discard\s+all\b)`)
	leadingWordsRe       = regexp.MustCompile(`(?is)^\s*([a-z]+)(?:\s+([a-z]+))?`)
)

// notNullCheck is one CHECK (<column> IS NOT NULL) constraint on a table.
type notNullCheck struct {
	column string
	valid  bool
}

// notNullProofs is the CHECK (<column> IS NOT NULL) constraints the
// migrations read so far leave in place, keyed by the normalized,
// schema-qualified-as-written table, then constraint name. A proof is
// established and used only on that exact table key.
//
// It is conservative by construction: a missed warning is the defect, an
// extra one is acceptable. Any statement the pass does not positively
// recognise as unable to remove or replace a table, column or constraint
// (harmlessStatement) forgets EVERY proof — DROP SCHEMA … CASCADE, a DO block
// or function that runs DDL, CREATE/DROP TABLE, anything unknown. Within an
// ALTER TABLE, a clause it does not understand forgets the table's proofs.
type notNullProofs map[string]map[string]*notNullCheck

// setNotNullKey is where the per-file rule reports a SET NOT NULL finding:
// its file, line and column. Two statements on one line share it, so the pass
// counts every SET NOT NULL clause per key and suppresses that key's findings
// only when every one of them was proven.
type setNotNullKey struct {
	file   string
	line   int
	column string
}

func dropCheckProvenSetNotNull(files []compatFile, findings []Finding) []Finding {
	checks := notNullProofs{}
	seen, proven := map[setNotNullKey]int{}, map[setNotNullKey]int{}
	for _, f := range files {
		for _, stmt := range migrationStatements(f.content) {
			m := alterTableNameRe.FindStringSubmatch(stmt.Text)
			if m == nil {
				if !harmlessStatement(stmt.Text) {
					clear(checks)
				}
				continue
			}
			table := normalizeSQLName(m[1])
			// Clauses of one ALTER TABLE apply in order, so a SET NOT NULL
			// in the same statement as the ADD CONSTRAINT (without NOT
			// VALID) is proven too: postgres validates the CHECK first.
			for _, clause := range splitAlterClauses(stmt.Text[len(m[0]):]) {
				switch {
				case addNotNullCheckRe.MatchString(clause):
					a := addNotNullCheckRe.FindStringSubmatch(clause)
					if checks[table] == nil {
						checks[table] = map[string]*notNullCheck{}
					}
					checks[table][normalizeSQLIdentifier(a[1])] = &notNullCheck{column: normalizeSQLIdentifier(firstNonEmpty(a[2], a[3])), valid: a[4] == ""}
				case validateConstraintRe.MatchString(clause):
					if c := checks[table][normalizeSQLIdentifier(validateConstraintRe.FindStringSubmatch(clause)[1])]; c != nil {
						c.valid = true
					}
				case dropConstraintRe.MatchString(clause):
					checks.forgetConstraint(table, normalizeSQLIdentifier(dropConstraintRe.FindStringSubmatch(clause)[1]))
				case setNotNullClauseRe.MatchString(clause):
					col := normalizeSQLIdentifier(setNotNullClauseRe.FindStringSubmatch(clause)[1])
					k := setNotNullKey{f.path, stmt.Line, col}
					seen[k]++
					for _, c := range checks[table] {
						if c.valid && c.column == col {
							proven[k]++
							break
						}
					}
				default:
					// DROP COLUMN, ALTER COLUMN … TYPE, RENAME, SET SCHEMA,
					// a CHECK this does not read: the table's proofs go.
					checks.forgetTable(table)
				}
			}
		}
	}
	kept := findings[:0:0]
	for _, f := range findings {
		if f.Rule == RuleSetNotNullWithoutBackfill {
			k := setNotNullKey{f.File, f.Line, setNotNullFindingColumn(f.Message)}
			if seen[k] > 0 && proven[k] == seen[k] {
				continue
			}
		}
		kept = append(kept, f)
	}
	return kept
}

// harmlessStatement reports whether a statement other than ALTER TABLE
// certainly removes or replaces no table, column or constraint, so the proofs
// survive it: plain DML, COMMENT, GRANT/REVOKE, index builds, and SET of a
// parameter known not to touch name resolution (harmlessSet). Anything
// else — DROP SCHEMA, CREATE or DROP TABLE, DO, CREATE FUNCTION, CALL, SELECT
// (which can call a function that runs DDL), unknown DDL — is not.
func harmlessStatement(text string) bool {
	if searchPathChangeRe.MatchString(text) {
		return false
	}
	w := leadingWordsRe.FindStringSubmatch(text)
	if w == nil {
		return strings.TrimSpace(text) == ""
	}
	first, second := strings.ToLower(w[1]), strings.ToLower(w[2])
	switch first {
	case "insert", "update", "delete", "comment", "grant", "revoke":
		return true
	case "set":
		return harmlessSet(text)
	case "create":
		return second == "index" || second == "unique"
	}
	return false
}

// harmlessSetParams are the run-time parameters a migration sets that
// certainly leave name resolution alone. A SET of any other parameter
// forgets the proofs: SET SCHEMA 'x' is SET search_path TO x, a quoted
// "search_path" is search_path, and an unknown one is not known to be safe.
var harmlessSetParams = map[string]bool{
	"lock_timeout": true, "statement_timeout": true, "idle_in_transaction_session_timeout": true,
	"client_min_messages": true, "client_encoding": true, "standard_conforming_strings": true,
	"check_function_bodies": true, "maintenance_work_mem": true, "work_mem": true,
	"synchronous_commit": true, "timezone": true, "time zone": true, "application_name": true,
	"row_security": true, "xmloption": true, "default_tablespace": true,
	"default_with_oids": true, "constraints": true, "transaction": true,
}

var setParamRe = regexp.MustCompile(`(?is)^\s*set\s+(?:(?:local|session)\s+)?("(?:[^"]|"")+"|[a-z_][a-z0-9_$.]*(?:\s+zone)?)`)

// harmlessSet reports whether a SET statement names a parameter in
// harmlessSetParams (unquoted or quoted, any case).
func harmlessSet(text string) bool {
	m := setParamRe.FindStringSubmatch(text)
	if m == nil {
		return false
	}
	name := strings.ToLower(m[1])
	if strings.HasPrefix(name, `"`) {
		name = strings.ReplaceAll(strings.Trim(name, `"`), `""`, `"`)
	}
	return harmlessSetParams[strings.Join(strings.Fields(name), " ")]
}

// An unqualified name may resolve to any schema with that table name. Use
// possible aliases only to invalidate proofs, never to establish a proof.
func (p notNullProofs) forgetConstraint(table, constraint string) {
	for other, byName := range p {
		if table == other || (unqualified(table) == unqualified(other) &&
			(table == unqualified(table) || other == unqualified(other))) {
			delete(byName, constraint)
		}
	}
}

// forgetTable drops every proof on table, and on any table of the same name
// in another schema: a qualified and an unqualified spelling may be one table.
func (p notNullProofs) forgetTable(table string) {
	for t := range p {
		if unqualified(t) == unqualified(table) {
			delete(p, t)
		}
	}
}

// Normalized qualified names use a NUL separator so a dot inside a quoted
// identifier cannot alias a real schema separator. SQL identifiers cannot
// contain NUL.
const schemaSeparator = "\x00"

func unqualified(table string) string {
	if _, name, ok := strings.Cut(table, schemaSeparator); ok {
		return name
	}
	return table
}

// setNotNullFindingColumn reads the column back out of a
// set-not-null-without-backfill message.
func setNotNullFindingColumn(message string) string {
	start := strings.Index(message, `column "`)
	if start < 0 {
		return ""
	}
	quoted, err := strconv.QuotedPrefix(message[start+len("column "):])
	if err != nil {
		return ""
	}
	col, _ := strconv.Unquote(quoted)
	return col
}

// splitAlterClauses splits on commas at the top level of an ALTER action
// list. PostgreSQL quotes, comments and bracketed expressions are not syntax
// at this level; the same quote lexers also split the containing statements.
func splitAlterClauses(actions string) []string {
	var clauses []string
	depth, start := 0, 0
	for i := 0; i < len(actions); i++ {
		if end, _ := migrationQuotedOrCommentEnd(actions, i); end > i {
			i = end - 1
			continue
		}
		switch actions[i] {
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				clauses = append(clauses, actions[start:i])
				start = i + 1
			}
		}
	}
	return append(clauses, actions[start:])
}

// normalizeSQLName folds an identifier the way postgres does: a quoted one
// keeps its case (quotes dropped), an unquoted one is lower-cased. A
// schema-qualified table keeps its schema.
func normalizeSQLName(name string) string {
	inQuote := false
	for i, r := range name {
		if r == '"' {
			inQuote = !inQuote
		}
		if r == '.' && !inQuote {
			return normalizeSQLIdentifier(name[:i]) + schemaSeparator + normalizeSQLIdentifier(name[i+1:])
		}
	}
	return normalizeSQLIdentifier(name)
}

func normalizeSQLIdentifier(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return name[1 : len(name)-1]
	}
	return strings.ToLower(name)
}
