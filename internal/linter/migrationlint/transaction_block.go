package migrationlint

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// A MIGRATION FILE IS ONE TRANSACTION.
//
// golang-migrate — and so forge's migrator, AutoMigrate and testkit — sends a
// migration file to postgres as ONE query. A query holding several statements
// runs as one implicit transaction. Two kinds of statement cannot live there,
// and each fails only when the migration runs, leaving the schema dirty:
//
//   - one postgres refuses inside any transaction block (SQLSTATE 25001):
//     CREATE INDEX CONCURRENTLY and the rest of nonTransactional's list. A
//     file holding exactly that one statement is fine — postgres runs a lone
//     statement outside any block — so the rule is "alone in its file", not
//     "never".
//   - the use of an enum value the same file ADDs (SQLSTATE 55P04, "unsafe use
//     of new value"): a value added inside a transaction cannot be used until
//     that transaction commits.
//
// ALTER TYPE … ADD VALUE itself is NOT on the first list. Postgres refused it
// inside a transaction block only before 12, which is past end of life, and
// every postgres forge provisions or tests against is 16 or newer (squash's
// image 16, the scaffolded compose 17, pgtest 18). On its own it runs
// anywhere. What breaks is using the value in the same file: the second rule.
//
// goose has a NO TRANSACTION mode for exactly these statements; golang-migrate
// has none. A goose history imported whole hit both at `migrate up` (P5a).

// RuleNonTransactionalStatement is the rule ID for a statement that cannot run
// inside a transaction block, in a migration file that holds other statements.
const RuleNonTransactionalStatement = "non-transactional-statement"

// NonTransactionalStatementRemediation is the fix text for a
// non-transactional-statement finding.
const NonTransactionalStatementRemediation = "give each statement that cannot run inside a transaction block — CREATE INDEX CONCURRENTLY, DROP INDEX CONCURRENTLY, REINDEX … CONCURRENTLY, VACUUM and the like — a migration file of its own (`forge db migration new <name>`), holding that one statement and nothing else. golang-migrate sends a migration file to postgres as one query: a lone statement runs outside any transaction block, several run as one implicit transaction. " + allowInTransactionHatch

// RuleEnumValueUsedBeforeCommit is the rule ID for a migration that uses an
// enum value it also adds.
const RuleEnumValueUsedBeforeCommit = "enum-value-used-before-commit"

// EnumValueUsedBeforeCommitRemediation is the fix text for an
// enum-value-used-before-commit finding.
const EnumValueUsedBeforeCommitRemediation = "move the ALTER TYPE … ADD VALUE into a migration of its own, versioned before the one that uses the value (`forge db migration new <name>`): each migration commits before the next one runs, so the value is committed by the time it is used. " + allowInTransactionHatch

// allowInTransactionHatch names the opt-out, for the cases the rules cannot
// see: both read statements, not the database, so a subscription created
// without a replication slot, or a string that only shares an added enum
// value's spelling, looks like the failing case. Colocated with the regex so
// the spelling we tell users cannot drift from the one we match.
const allowInTransactionHatch = `If postgres does run this file as one transaction (a subscription with no replication slot; a string that only shares the enum value's spelling), mark the migration file with a "-- forge:allow-in-transaction: <why>" comment`

// allowInTransactionPragmaRe matches the in-file opt-out for both rules.
var allowInTransactionPragmaRe = regexp.MustCompile(`(?im)^\s*--\s*forge:allow-in-transaction\b`)

// lintTransactionBlock reports the statements of one migration file that fail
// because the file runs as one transaction. See the comment above.
func lintTransactionBlock(file, content string) []Finding {
	if allowInTransactionPragmaRe.MatchString(content) {
		return nil
	}
	stmts := lexStatements(content)
	var findings []Finding
	if len(stmts) > 1 {
		for _, s := range stmts {
			label := nonTransactional(s.Words)
			if label == "" {
				continue
			}
			findings = append(findings, Finding{File: file, Line: s.Line, Rule: RuleNonTransactionalStatement, Severity: SeverityError,
				Message: fmt.Sprintf("%s cannot run inside a transaction block, and this migration holds %d statements: golang-migrate sends a migration file to postgres as one query, which postgres runs as one implicit transaction, so the file fails with SQLSTATE 25001 and leaves the schema dirty — move it to a migration file of its own, as its only statement (`forge db migration new <name>`)",
					label, len(stmts))})
		}
	}
	return append(findings, lintEnumValueUse(file, stmts)...)
}

// nonTransactional returns how postgres names a statement it refuses inside a
// transaction block — the callers of PreventInTransactionBlock — or "" for
// any other statement. Only statements a migration could plausibly hold are
// listed; COMMIT PREPARED and the like are not.
//
// Each case matches the statement's leading keywords, not text anywhere in
// it, so a comment or a string that mentions CONCURRENTLY never matches.
func nonTransactional(w []string) string {
	at := func(i int, word string) bool { return len(w) > i && w[i] == word }
	has := func(word string) bool { return slices.Contains(w, word) }
	switch {
	case at(0, "create") && (at(1, "index") && at(2, "concurrently") || at(1, "unique") && at(2, "index") && at(3, "concurrently")):
		return "CREATE INDEX CONCURRENTLY"
	case at(0, "drop") && at(1, "index") && at(2, "concurrently"):
		return "DROP INDEX CONCURRENTLY"
	case at(0, "reindex") && has("concurrently") && !optionOff(w, "concurrently"):
		return "REINDEX … CONCURRENTLY"
	case at(0, "reindex"):
		switch kind := reindexKind(w); kind {
		case "schema", "database", "system":
			return "REINDEX " + strings.ToUpper(kind)
		}
	case at(0, "alter") && at(1, "table") && has("detach") && has("concurrently"):
		return "ALTER TABLE … DETACH PARTITION … CONCURRENTLY"
	case at(0, "vacuum"):
		return "VACUUM"
	case at(0, "cluster") && !slices.ContainsFunc(w[1:], func(s string) bool { return s != "verbose" }):
		// CLUSTER with no table reclusters every table, one transaction each.
		return "CLUSTER without a table"
	case at(0, "create") && at(1, "database"):
		return "CREATE DATABASE"
	case at(0, "drop") && at(1, "database"):
		return "DROP DATABASE"
	case at(0, "alter") && at(1, "database") && hasSeq(w, "set", "tablespace"):
		return "ALTER DATABASE … SET TABLESPACE"
	case at(0, "create") && at(1, "tablespace"):
		return "CREATE TABLESPACE"
	case at(0, "drop") && at(1, "tablespace"):
		return "DROP TABLESPACE"
	case at(0, "alter") && at(1, "system"):
		return "ALTER SYSTEM"
	case at(0, "create") && at(1, "subscription") && !optionOff(w, "connect") && !optionOff(w, "create_slot"):
		return "CREATE SUBSCRIPTION (it creates a replication slot)"
	case at(0, "drop") && at(1, "subscription"):
		return "DROP SUBSCRIPTION (of a subscription with a replication slot)"
	case at(0, "alter") && at(1, "subscription") && !optionOff(w, "refresh") &&
		(has("refresh") || has("publication") && (has("set") || has("add") || has("drop"))):
		return "ALTER SUBSCRIPTION … with refresh"
	case at(0, "discard") && at(1, "all"):
		return "DISCARD ALL"
	}
	return ""
}

// reindexKind returns the object kind a REINDEX names — index, table, schema,
// database or system — past any parenthesised options.
func reindexKind(w []string) string {
	for _, word := range w[1:] {
		switch word {
		case "index", "table", "schema", "database", "system":
			return word
		}
	}
	return ""
}

// optionOff reports whether a statement turns option off: `option = false`,
// lexed as the two words.
func optionOff(w []string, option string) bool {
	i := slices.Index(w, option)
	return i >= 0 && i+1 < len(w) && (w[i+1] == "false" || w[i+1] == "off")
}

// hasSeq reports whether w holds a then b, adjacent.
func hasSeq(w []string, a, b string) bool {
	for i := 0; i+1 < len(w); i++ {
		if w[i] == a && w[i+1] == b {
			return true
		}
	}
	return false
}

// lintEnumValueUse reports a statement that uses an enum value an earlier
// statement of the same file adds. A value added to a type the same file
// CREATEs is safe: postgres only refuses a new value of a type that existed
// before the transaction. An ALTER TYPE naming the value (ADD VALUE … AFTER
// it) is not a use.
//
// A use is the value as a string constant. A same-spelled string meant for
// something else is a false positive, and its fix — the ADD VALUE in an
// earlier migration — is harmless.
func lintEnumValueUse(file string, stmts []lexedStatement) []Finding {
	type added struct {
		enum, value string
		line        int
	}
	var (
		adds     []added
		created  = map[string]bool{}
		findings []Finding
	)
	for _, s := range stmts {
		if name, ok := createdEnum(s.Words); ok {
			created[name] = true
			continue
		}
		if name, ok := alteredType(s.Words); ok {
			if slices.Contains(s.Words, "add") && slices.Contains(s.Words, "value") && len(s.Strings) > 0 && !created[name] {
				adds = append(adds, added{enum: name, value: s.Strings[0], line: s.Line})
			}
			continue
		}
		for _, a := range adds {
			if !slices.Contains(s.Strings, a.value) {
				continue
			}
			findings = append(findings, Finding{File: file, Line: s.Line, Rule: RuleEnumValueUsedBeforeCommit, Severity: SeverityError,
				Message: fmt.Sprintf("uses enum value '%s', which line %d adds to %s in this same migration: a value added inside a transaction cannot be used until it commits, and golang-migrate runs the whole file as one implicit transaction, so the file fails with SQLSTATE 55P04 (unsafe use of new value) and leaves the schema dirty — move the ALTER TYPE … ADD VALUE to an earlier migration of its own",
					a.value, a.line, a.enum)})
		}
	}
	return findings
}

// createdEnum returns the name of the enum type a CREATE TYPE … AS ENUM
// statement creates.
func createdEnum(w []string) (string, bool) {
	if len(w) < 4 || w[0] != "create" || w[1] != "type" {
		return "", false
	}
	as := slices.Index(w, "as")
	if as < 3 || as+1 >= len(w) || w[as+1] != "enum" {
		return "", false
	}
	return typeName(w[as-1]), true
}

// alteredType returns the name of the type an ALTER TYPE statement alters.
func alteredType(w []string) (string, bool) {
	if len(w) < 3 || w[0] != "alter" || w[1] != "type" {
		return "", false
	}
	// The name is the last word before the action: `alter type s.t add …`
	// lexes as alter, type, s, t, add.
	for i := 2; i < len(w); i++ {
		switch w[i] {
		case "add", "rename", "owner", "set", "drop", "alter":
			if i > 2 {
				return typeName(w[i-1]), true
			}
			return "", false
		}
	}
	return "", false
}

// typeName folds a type name to how postgres resolves it: a bare word is
// already lowercased, a quoted one is taken as written.
func typeName(word string) string {
	return strings.Trim(word, `"`)
}
