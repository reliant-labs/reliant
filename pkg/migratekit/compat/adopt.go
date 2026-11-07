package compat

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ADOPTION — a baseline that replaces another tool's migration history.
//
// A project that moves onto forge from another migration tool squashes that
// tool's history into ONE baseline migration. A fresh database applies the
// baseline as SQL. A database the other tool already migrated must not: its
// schema already IS the baseline, and running it would fail on the first
// CREATE TABLE, or — for DDL written IF NOT EXISTS — half-succeed. So the
// baseline names the history it replaces, in the file, every version listed:
//
//	-- forge:adopts goose 20260211000002 20260211000003 20260212100000
//	-- forge:adopts goose 20261011194043 20261011203444
//
// The line may repeat and the lists accumulate, so a long history wraps. The
// migrator (pkg/migratekit) adopts a database whose recorded history is
// EXACTLY this set — it records the baseline as applied and runs none of its
// SQL — and refuses one that is behind (a listed version it never applied) or
// that holds a version the list does not account for.
//
// The tool may be forge itself. `forge db squash` collapses a forge history
// into one baseline, and the databases that history built already have the
// baseline's schema, recorded in forge's own tables:
//
//	-- forge:adopts forge 20260101000001 20260101000002
//
// The migrator adopts such a database on the same terms: every listed version
// recorded as applied, and its schema version one of them.
//
// WHY EVERY VERSION AND NOT THE LAST ONE. goose applies out of order
// (WithAllowMissing), so "the newest applied version is X" does not mean every
// version below X ran: a migration merged with an EARLIER timestamp after a
// database last migrated sits below X and was never applied there. Nothing in
// the database records which versions EXIST — only the file list the baseline
// replaced does, so the baseline carries it. A highest-version check would
// adopt precisely the database it has to refuse.
//
// WHY A DECLARATION IN THE FILE. Adoption is a fact about the baseline — "this
// SQL is what that history produced" — so it lives beside the SQL, is
// reviewed with it, and travels in the binary's embedded migrations to every
// database the binary meets: the deploy-time initContainer, boot-time
// AutoMigrate on a desktop install, and `db migrate up` alike, with no step
// for anyone to run.

// AdoptsDirective is the line a baseline carries to declare the history it
// replaces, followed by the tool and its versions. Exported so the linter,
// errors and docs quote the spelling the parser matches.
const AdoptsDirective = "-- forge:adopts"

// ToolGoose names pressly/goose, whose history lives in goose_db_version.
const ToolGoose = "goose"

// ToolForge names forge's own migrator: a history `forge db squash` replaced,
// recorded in schema_migrations and schema_migrations_applied.
const ToolForge = "forge"

// adoptableTools is every tool whose history the migrator can read. A tool
// is added here together with its reader in pkg/migratekit; the directive's
// grammar does not change.
var adoptableTools = []string{ToolGoose, ToolForge}

// adoptsRe matches any `forge:adopt…` line comment, so that a misspelling of
// the one directive whose ABSENCE is dangerous is an error rather than a
// silently ignored comment: a baseline that fails to declare its adoption
// runs its SQL against a database that already has the schema.
//
// Only a LINE COMMENT counts, as with Directive.
var adoptsRe = regexp.MustCompile(`(?i)^\s*--\s*forge:(adopt\S*)(.*)$`)

// Adoption is the foreign history one migration declares it replaces.
type Adoption struct {
	// Tool is the migration tool whose history this replaces (ToolGoose).
	Tool string
	// Versions is every version that tool recorded for the history this
	// migration replaces: ascending, distinct, each positive.
	Versions []int64
	// Line is the 1-based line of the first directive, for messages.
	Line int
}

// AdoptionSyntaxError is a malformed adoption directive. It carries the line so
// the linter can point at it; the migrator refuses the whole migration set,
// because a baseline whose adoption cannot be read would run its SQL against a
// database that already has the schema.
type AdoptionSyntaxError struct {
	Line   int
	Reason string
}

func (e *AdoptionSyntaxError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

// ParseAdoption reads the adoption one migration's up SQL declares. No
// directive is (nil, nil): almost every migration adopts nothing.
//
// The grammar is strict, and deliberately so. Every database the binary meets
// parses it at boot — a fresh test database included — so a malformed list
// fails the first CI run instead of waiting for the one production database
// it was written for.
func ParseAdoption(upSQL string) (*Adoption, error) {
	var (
		out  *Adoption
		seen = map[int64]bool{}
	)
	err := directiveLines(upSQL, func(line int, text string) error {
		m := adoptsRe.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		if !strings.EqualFold(m[1], "adopts") {
			return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("unknown directive `forge:%s` — the adoption directive is spelled %q", m[1], AdoptsDirective)}
		}
		fields := strings.Fields(m[2])
		if len(fields) == 0 {
			return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("%q names no tool — write `%s <tool> <version>...` (tools: %s)", strings.TrimSpace(text), AdoptsDirective, strings.Join(adoptableTools, ", "))}
		}
		tool := strings.ToLower(fields[0])
		if !adoptable(tool) {
			return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("cannot adopt %q history — the migrator reads only: %s", fields[0], strings.Join(adoptableTools, ", "))}
		}
		if out != nil && out.Tool != tool {
			return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("this line adopts %s history but line %d adopts %s — one baseline replaces one tool's history", tool, out.Line, out.Tool)}
		}
		if len(fields) == 1 {
			return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("`%s %s` lists no versions — list every version %s recorded for the history this baseline replaces", AdoptsDirective, tool, tool)}
		}
		if out == nil {
			out = &Adoption{Tool: tool, Line: line}
		}
		for _, tok := range fields[1:] {
			v, err := strconv.ParseInt(tok, 10, 64)
			if err != nil || v <= 0 {
				return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("%q is not a %s version (a positive integer)", tok, tool)}
			}
			if seen[v] {
				return &AdoptionSyntaxError{Line: line, Reason: fmt.Sprintf("version %d is listed twice — the list is generated from the replaced migration files, and a repeat means it was not", v)}
			}
			seen[v] = true
			out.Versions = append(out.Versions, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out != nil {
		sort.Slice(out.Versions, func(i, j int) bool { return out.Versions[i] < out.Versions[j] })
	}
	return out, nil
}

func adoptable(tool string) bool {
	for _, t := range adoptableTools {
		if t == tool {
			return true
		}
	}
	return false
}
