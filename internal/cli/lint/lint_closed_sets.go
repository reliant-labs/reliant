// closed-sets — `forge lint --closed-sets`, and part of every `forge lint`.
//
// A CHECK that mirrors a Go set declares it in the schema:
//
//	COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
//	    'forge:closed-set accesstoken.AllScopes';
//
// and this lane compares the value list of the CHECK the migrations last
// defined with that set (internal/closedset). It reads migration text, so it
// needs no database and runs on every tier lint runs on: the project-local
// test it replaces needed a database, skipped under -short, and let a forge
// bump that grew the scope set reach a project's main green. A mismatch
// GATES — a value Go grants and the CHECK refuses fails every write of it —
// and the finding carries the forward migration that fixes it.

package lint

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/closedset"
	"github.com/reliant-labs/forge/internal/config"
)

// closedSetFindings analyzes the project's migrations; cwd is the project
// root, which a project-owned set's package is loaded from.
func closedSetFindings(cwd string, cfg *config.ProjectConfig) ([]closedset.Finding, error) {
	dir := migrationsDirFor(cfg)
	fs, err := closedset.Analyze(dir, closedset.NewResolver(cwd))
	if err != nil {
		return nil, err
	}
	for i := range fs {
		if rel, err := filepath.Rel(cwd, fs[i].File); err == nil && cwd != "" && !strings.HasPrefix(rel, "..") {
			fs[i].File = rel
		}
	}
	return fs, nil
}

// closedSetFixHint is the remediation: the command that writes the fix, and
// the fix itself to paste.
func closedSetFixHint(f closedset.Finding) string {
	if f.Fix == nil {
		return "fix the COMMENT ON CONSTRAINT or the CHECK it names (`forge project annotations --kind column` documents forge:closed-set)"
	}
	return fmt.Sprintf("write the fix with `forge db migration new --closed-set %s`, or paste this into a new migration:\n%s",
		f.Constraint, indent(f.Fix.Migration, "        "))
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// collectClosedSetsJSON maps the findings onto the JSON contract. Severity
// error: a CHECK narrower than its set fails every write of the missing
// values, and one wider admits rows Go cannot read.
func collectClosedSetsJSON(cwd string, cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	fs, err := closedSetFindings(cwd, cfg)
	if err != nil {
		return nil, fmt.Errorf("closed-sets lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, lintJSONFinding{
			File:     f.File,
			Line:     f.Line,
			Severity: lintSevError,
			Rule:     "forge-closed-set",
			Message:  f.Problem,
			FixHint:  closedSetFixHint(f),
		})
	}
	return out, nil
}
