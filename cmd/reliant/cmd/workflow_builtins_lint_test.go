// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// lintCleanBuiltins are the builtins `reliant workflow lint` passes with no
// finding at all, with their scenarios and refs as the binary carries them.
// A builtin is what users copy, so the set only grows: a change that gives
// one of these a finding fails here, and a builtin that becomes clean must be
// added (the test says which).
var lintCleanBuiltins = map[string]bool{
	"autopilot-oncall":          true,
	"autopilot-security-review": true,
	"build-workflow":            true,
	"fix-flaky-test":            true,
	"forge-migrate":             true,
	"forge-one-shot":            true,
	"raise-coverage":            true,
	"vuln-patch":                true,
	"vuln-patch-agent":          true,
}

func TestLintCleanBuiltinsStayClean(t *testing.T) {
	layout, err := workflowref.BuiltinLayout()
	require.NoError(t, err)
	for _, name := range embeddedBuiltinNames(t) {
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		require.NoError(t, err)
		findings := lintWorkflowSource(data, layout)
		switch {
		case lintCleanBuiltins[name] && len(findings) > 0:
			var lines []string
			for _, f := range findings {
				lines = append(lines, f.String())
			}
			t.Errorf("builtin %s must lint clean (fix the finding, or allow it with its reason):\n  %s", name, strings.Join(lines, "\n  "))
		case !lintCleanBuiltins[name] && len(findings) == 0:
			t.Errorf("builtin %s now lints clean: add it to lintCleanBuiltins so it stays that way", name)
		}
	}
	for name := range lintCleanBuiltins {
		_, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		require.NoError(t, err, "lintCleanBuiltins names %s, which is not a builtin", name)
	}
}
