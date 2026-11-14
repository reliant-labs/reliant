// File: internal/cli/lint/grade_external.go
//
// Grading a forge rule that a command OUTSIDE `forge lint` reports.
//
// `forge db analyze` needs Postgres, so it is never a lint lane, but its rules
// are forge's own and must behave like every other: warn first through the
// rollout table, and answer to lint.rules. GradeFinding is that grading, the
// same code a lane's findings go through (gradeProjectLane), for one finding.

package lint

import "github.com/reliant-labs/forge/internal/config"

// GradeFinding grades one finding of a gradable rule for the project cfg
// describes: the warn-first rollout for its pinned forge, then lint.rules.
// severity is "error" or "warning". keep is false when lint.rules turns the
// rule off; note explains a softened severity ("" when nothing softened it).
func GradeFinding(cfg *config.ProjectConfig, rule, severity string) (graded string, keep bool, note string) {
	out, _ := gradeProjectLane(cfg, []lintJSONFinding{{Rule: rule, Severity: severity}}, severity == lintSevError)
	if len(out) == 0 {
		return "", false, ""
	}
	if g := out[0].Grace; g != nil {
		note = g.note(rule)
	}
	return out[0].Severity, true, note
}
