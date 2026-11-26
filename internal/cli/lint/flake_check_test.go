package lint

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// For customers flake-check reports and never fails, at any pin; lint.rules
// opts in.
func TestFlakeCheckIsAdvisoryUntilLintRulesRaisesIt(t *testing.T) {
	for _, c := range []struct {
		name  string
		cfg   *config.ProjectConfig
		gates bool
	}{
		{"no config", nil, false},
		{"no dial", &config.ProjectConfig{ForgeVersion: "v1.20.0"}, false},
		{"error", &config.ProjectConfig{Lint: config.LintConfig{Rules: map[string]string{"flake-check": "error"}}}, true},
		{"warn", &config.ProjectConfig{Lint: config.LintConfig{Rules: map[string]string{"flake-check": "warn"}}}, false},
		{"off", &config.ProjectConfig{Lint: config.LintConfig{Rules: map[string]string{"flake-check": "off"}}}, false},
		{"wildcard", &config.ProjectConfig{Lint: config.LintConfig{Rules: map[string]string{"*": "error"}}}, true},
	} {
		if got := FlakeCheckGates(c.cfg); got != c.gates {
			t.Errorf("%s: gates = %v, want %v", c.name, got, c.gates)
		}
	}
	if ro, ok := lintRollouts[RuleFlakeCheck]; !ok || ro.GatesFrom != "" {
		t.Errorf("flake-check's row must be advisory (no GatesFrom): %+v", ro)
	}
}
