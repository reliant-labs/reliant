package migrationlint

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/migrationver"
	"github.com/reliant-labs/forge/pkg/migratekit/compat"
)

// RuleInvalidAdoption is the rule ID for an adoption declaration
// (`-- forge:adopts <tool> <versions>`) the migrator would refuse: one it
// cannot read, or one on a migration that is not the first.
//
// The migrator refuses both on EVERY database, at Open and at boot, so the
// first test run fails either way. The rule moves that to review time and
// points at the line. It matters more than most lint because of what the
// declaration guards: a baseline whose adoption does not take effect runs its
// SQL against a database another tool already migrated.
const RuleInvalidAdoption = "invalid-adopts-directive"

// InvalidAdoptionRemediation is the fix text for an invalid-adopts-directive
// finding.
const InvalidAdoptionRemediation = `declare the replaced history on the FIRST migration only — the baseline that squashed it — as ` + "`" + compat.AdoptsDirective + ` <tool> <version> <version> ...` + "`" + ` lines listing every version that history recorded (repeat the line to wrap a long list): tool goose for a goose history, forge for one ` + "`forge db squash`" + ` collapsed, whose listed versions are then files the directory no longer holds. See the forge skill db/deploy-migrations, "Adopting a database another tool migrated" and "Squashing a forge history"`

// adoptingFile is one migration whose body declares an adoption.
type adoptingFile struct {
	file     string
	version  uint64
	line     int
	tool     string
	versions []int64
}

// lintAdoption checks one migration's adoption declaration, and returns it
// for the set-level placement check when it is well formed.
func lintAdoption(file, content string) (*adoptingFile, []Finding) {
	adoption, err := compat.ParseAdoption(content)
	if err != nil {
		line := 1
		var syn *compat.AdoptionSyntaxError
		if errors.As(err, &syn) {
			line = syn.Line
		}
		return nil, []Finding{{File: file, Line: line, Rule: RuleInvalidAdoption, Severity: SeverityError,
			Message: fmt.Sprintf("the migrator cannot read this adoption declaration and refuses every database while it is here: %v", err)}}
	}
	if adoption == nil {
		return nil, nil
	}
	v, ok := migrationver.ParseVersion(filepath.Base(file))
	if !ok {
		return nil, nil
	}
	return &adoptingFile{file: file, version: v, line: adoption.Line, tool: adoption.Tool, versions: adoption.Versions}, nil
}

// lintAdoptionPlacement reports every adoption declared on a migration that is
// not the lowest-versioned in the directory. A database another tool migrated
// has run none of this project's migrations, so only the baseline every other
// migration builds on can already be reflected in it.
func lintAdoptionPlacement(files []string, adopting []adoptingFile) []Finding {
	if len(adopting) == 0 {
		return nil
	}
	var (
		first     uint64
		firstFile string
	)
	for _, f := range files {
		v, ok := migrationver.ParseVersion(filepath.Base(f))
		if ok && (firstFile == "" || v < first) {
			first, firstFile = v, f
		}
	}
	present := map[uint64]bool{}
	for _, f := range files {
		if v, ok := migrationver.ParseVersion(filepath.Base(f)); ok {
			present[v] = true
		}
	}
	var findings []Finding
	for _, a := range adopting {
		if a.version == first {
			// A squashed forge history lists versions this directory no
			// longer holds; the migrator refuses a listed version it still
			// embeds (the baseline's own, or a file left beside it).
			if a.tool == compat.ToolForge {
				var carried []string
				for _, v := range a.versions {
					if v > 0 && present[uint64(v)] {
						carried = append(carried, fmt.Sprint(v))
					}
				}
				if len(carried) > 0 {
					findings = append(findings, Finding{File: a.file, Line: a.line, Rule: RuleInvalidAdoption, Severity: SeverityError,
						Message: fmt.Sprintf("this baseline declares it replaces forge version(s) %s, which this directory still holds — a squash replaces the migrations it lists, so the migrator refuses every database while a listed version is the baseline's own or a file beside it: number the baseline outside the squashed history and move the squashed files out",
							strings.Join(carried, ", "))})
				}
			}
			continue
		}
		findings = append(findings, Finding{File: a.file, Line: a.line, Rule: RuleInvalidAdoption, Severity: SeverityError,
			Message: fmt.Sprintf("only the first migration may declare %q, and %s comes before this one — a database another tool migrated has run none of this project's migrations, so declaring the adoption here would record %s as applied there without running it",
				compat.AdoptsDirective, filepath.Base(firstFile), filepath.Base(firstFile))})
	}
	return findings
}
