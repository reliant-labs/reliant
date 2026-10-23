package migrationlint

import (
	"fmt"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/migrationver"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// Rule IDs of the compatibility-declaration rules.
const (
	// RuleCompatConflict: one migration declares both that the previous
	// release keeps working and that it does not. Always on — the file
	// contradicts itself, and the migrator reads it the safe way (not
	// compatible), which is not what half of it says.
	RuleCompatConflict = "migration-compat-conflict"
	// RuleCompatUndeclared: with database.migration_safety.
	// require_compat_declaration on, a migration after the baseline says
	// neither.
	RuleCompatUndeclared = "migration-compat-undeclared"
	// RuleCompatReason: with the requirement on, the declaration carries no
	// reason (or only a `<why …>` placeholder).
	RuleCompatReason = "migration-compat-reason"
)

// WHY A PROJECT MAY REQUIRE THE DECLARATION.
//
// Every rolling deploy runs the previous release's code on the next one's
// schema, and the migrator lets an older binary serve over a newer schema
// only when each newer migration declared `-- forge:backward-compatible`
// (pkg/migratekit/compat). Saying nothing is read as "not compatible", which
// is the safe reading at runtime — and indistinguishable, in review, from a
// migration whose author never thought about it. A project that wants every
// migration to be a decision turns the requirement on: each one then says
// either `-- forge:backward-compatible — <why>` or `-- forge:breaks-rollback
// <why>`, and the reason is what a reviewer evaluates.
//
// The requirement starts after the BASELINE: the newest migration that adopts
// a history (`-- forge:adopts`, which `forge db squash` and a goose cutover
// write), or, with none, the first migration. A baseline builds a database;
// no older release of THIS migration set ever runs on what it leaves.

// compatFile is one up migration as lintCompatDeclarations reads it.
type compatFile struct {
	path    string
	content string
}

// lintCompatDeclarations applies the declaration rules over the whole set.
func lintCompatDeclarations(files []compatFile, adopting []adoptingFile, require bool) []Finding {
	baseline, haveBaseline := uint64(0), false
	for _, a := range adopting {
		if !haveBaseline || a.version > baseline {
			baseline, haveBaseline = a.version, true
		}
	}
	if !haveBaseline {
		for _, f := range files {
			if v, ok := migrationver.ParseVersion(filepath.Base(f.path)); ok && (!haveBaseline || v < baseline) {
				baseline, haveBaseline = v, true
			}
		}
	}

	var findings []Finding
	for _, f := range files {
		compatible, breaks := compat.Declarations(f.content)
		if len(compatible) > 0 && len(breaks) > 0 {
			findings = append(findings, Finding{
				File: f.path, Line: breaks[0].Line, Rule: RuleCompatConflict, Severity: SeverityError,
				Message: fmt.Sprintf("this migration declares both %q (line %d) and %q: it is one or the other. "+
					"The migrator reads it as NOT backward-compatible", compat.Directive, compatible[0].Line,
					compat.BreaksRollbackDirective),
			})
			continue
		}
		if !require {
			continue
		}
		v, ok := migrationver.ParseVersion(filepath.Base(f.path))
		if !ok || v <= baseline {
			continue
		}
		var decl []compat.Declaration
		switch {
		case len(compatible) > 0:
			decl = compatible
		case len(breaks) > 0:
			decl = breaks
		default:
			findings = append(findings, Finding{
				File: f.path, Line: 1, Rule: RuleCompatUndeclared, Severity: SeverityError,
				Message: fmt.Sprintf("this migration does not say whether the previous release keeps working on the schema it "+
					"leaves (this project sets database.migration_safety.require_compat_declaration). Add `%s — <why>` if it "+
					"does (a column, table or index old code ignores), or `%s <why>` if it deliberately does not (a dropped or "+
					"renamed column old code reads, a NOT NULL column old code does not write)",
					compat.Directive, compat.BreaksRollbackDirective),
			})
			continue
		}
		if decl[0].Reason == "" {
			findings = append(findings, Finding{
				File: f.path, Line: decl[0].Line, Rule: RuleCompatReason, Severity: SeverityError,
				Message: "the compatibility declaration gives no reason: write on the same line why the previous release " +
					"keeps working on this schema (or why, deliberately, it does not) — that is what review evaluates",
			})
		}
	}
	return findings
}
