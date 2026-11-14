package lint

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/pkg/dbanalyze"
)

func TestGradeFindingWarnsFirstAndHonoursLintRules(t *testing.T) {
	rule := dbanalyze.RuleTableRewrite
	ro := lintRollouts[rule]
	if ro.GatesFrom == "" {
		t.Fatalf("%s has no warn-first rollout", rule)
	}

	behind := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: ro.IntroducedIn}
	sev, keep, note := GradeFinding(behind, rule, lintSevError)
	if sev != lintSevWarning || !keep || note == "" {
		t.Errorf("pinned at %s: %q %v %q, want a warning with a note", ro.IntroducedIn, sev, keep, note)
	}

	at := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: ro.GatesFrom}
	if sev, keep, note := GradeFinding(at, rule, lintSevError); sev != lintSevError || !keep || note != "" {
		t.Errorf("pinned at %s: %q %v %q, want an error", ro.GatesFrom, sev, keep, note)
	}

	off := &config.ProjectConfig{ModulePath: "example.com/app", ForgeVersion: ro.GatesFrom,
		Lint: config.LintConfig{Rules: map[string]string{rule: suppress.SeverityOff}}}
	if _, keep, _ := GradeFinding(off, rule, lintSevError); keep {
		t.Error("lint.rules {rule: off} kept the finding")
	}
}
