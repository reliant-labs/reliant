package lint

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// Graph findings answer to forge.yaml's lint.rules like every forge rule —
// through the outcome engine's one grading pass — so a project declaring its
// roots one at a time can hold the unreachable rule at warn until the last
// root lands.
func TestGraphFindings_LintRulesSetTheSeverity(t *testing.T) {
	findings := func() []lintJSONFinding {
		return []lintJSONFinding{
			{Rule: codegen.GraphRuleUnreachable, Severity: lintSevError, File: "internal/handlers/alpha", Message: "unreached"},
			{Rule: codegen.GraphRuleAudience, Severity: lintSevError, File: "internal/app/providers.go", Message: "audience"},
			{Rule: codegen.GraphRuleUnreachable, Severity: lintSevWarning, File: "internal/blobstore", Message: "pending"},
		}
	}
	grade := func(rules map[string]string) (string, bool) {
		got, gated := gradeLane(config.LintConfig{Rules: rules}.EffectiveRules(), findings(), true)
		var out []string
		for _, f := range got {
			sev := "warn"
			if f.Severity == lintSevError {
				sev = "error"
			}
			out = append(out, f.Message+"/"+sev)
		}
		return strings.Join(out, ", "), gated
	}
	cases := []struct {
		name      string
		rules     map[string]string
		want      string
		wantGated bool
	}{
		{"default: the analyzer's severity", nil, "unreached/error, audience/error, pending/warn", true},
		{"warn stops the gate", map[string]string{"forge-graph-unreachable": "warn", "forge-graph-audience": "warn"}, "unreached/warn, audience/warn, pending/warn", false},
		{"rule ids are case-insensitive", map[string]string{"Forge-Graph-Unreachable": "warn"}, "unreached/warn, audience/error, pending/warn", true},
		{"off drops the finding", map[string]string{"forge-graph-unreachable": "off"}, "audience/error", true},
		{"error raises a warning", map[string]string{"forge-graph-unreachable": "error"}, "unreached/error, audience/error, pending/error", true},
		{"an exact id beats the wildcard", map[string]string{"*": "warn", "forge-graph-audience": "error"}, "unreached/warn, audience/error, pending/warn", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gated := grade(tc.rules)
			if got != tc.want || gated != tc.wantGated {
				t.Fatalf("got  %s gated=%v\nwant %s gated=%v", got, gated, tc.want, tc.wantGated)
			}
		})
	}
}
