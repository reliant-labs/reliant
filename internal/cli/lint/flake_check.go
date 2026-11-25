package lint

import (
	"github.com/reliant-labs/forge/internal/config"
)

// RuleFlakeCheck is the `forge ci gate` flake-check step's rule: a test the
// change adds or edits failed one of its three runs (`forge test flakes
// gate`). It is advisory at any pin — for customers the step reports and
// never fails — and `lint.rules: {flake-check: error}` makes it block.
const RuleFlakeCheck = "flake-check"

// FlakeCheckGates reports whether a failing flake-check blocks this
// project's gate: its findings are warnings unless lint.rules raises the rule
// (by name, or `*`), graded by the same code every lint lane's findings are.
func FlakeCheckGates(cfg *config.ProjectConfig) bool {
	fs, _ := gradeProjectLane(cfg, []lintJSONFinding{{Rule: RuleFlakeCheck, Severity: lintSevWarning}}, false)
	return len(fs) == 1 && fs[0].Severity == lintSevError
}
