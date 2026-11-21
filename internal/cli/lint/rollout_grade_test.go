package lint

import (
	"strings"
	"testing"
)

// GradeRule is the warn-first promise for a rule that judges something other
// than source (a promotion, a gate step): the same row, pin and lint.rules
// semantics applyRollout gives a finding.
func TestGradeRule(t *testing.T) {
	prevHistory := previousPinFromHistory
	t.Cleanup(func() { previousPinFromHistory = prevHistory })

	cases := []struct {
		name      string
		pin       string
		rules     map[string]string
		previous  string // "" = history unknown
		wantGates bool
		wantNote  string
		wantDial  string
	}{
		{name: "below GatesFrom warns", pin: "v1.0.0", wantNote: "new in v1.0.0; gates from v1.0.1"},
		{name: "a pre-release of the introducing release warns", pin: "v1.0.0-rc.1", wantNote: "gates from v1.0.1"},
		{name: "at GatesFrom gates", pin: "v1.0.1", wantGates: true},
		{name: "past GatesFrom gates", pin: "v1.4.0", wantGates: true},
		{name: "a pseudo-version leading to GatesFrom gates", pin: "v1.0.1-0.20261120000000-abcdef123456", wantGates: true},
		{name: "unpinned gates: nothing says the project is behind", pin: "", wantGates: true},
		{name: "the 0.0.0 sentinel gates", pin: "0.0.0", wantGates: true},
		{name: "error opts in early", pin: "v1.0.0", rules: map[string]string{rolloutRule: "error"}, wantGates: true, wantDial: "error"},
		{name: "warn extends the grace period", pin: "v1.9.0", rules: map[string]string{rolloutRule: "warn"}, wantNote: "lint.rules sets " + rolloutRule + " to warn", wantDial: "warn"},
		{name: "off never gates", pin: "v1.9.0", rules: map[string]string{rolloutRule: "off"}, wantDial: "off", wantNote: "to off"},
		{name: "the wildcard dials it", pin: "v1.9.0", rules: map[string]string{"*": "warn"}, wantDial: "warn"},
		{name: "an exact key beats the wildcard", pin: "v1.0.0", rules: map[string]string{"*": "warn", rolloutRule: "error"}, wantGates: true, wantDial: "error"},
		{name: "an unparseable value falls back to the rollout", pin: "v1.0.0", rules: map[string]string{rolloutRule: "loud"}, wantNote: "gates from v1.0.1"},
		{name: "a pin that jumped the window warns for that pin", pin: "v1.3.0", previous: "v0.1.40", wantNote: "gates from the next forge pin after v1.3.0"},
		{name: "a pin that went through the window gates", pin: "v1.3.0", previous: "v1.0.0", wantGates: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previousPinFromHistory = func(string, string) (string, bool) { return tc.previous, tc.previous != "" }
			g := gradeRuleWith(testRollouts, pinnedCfg(tc.pin, tc.rules), rolloutRule)
			if g.Gates != tc.wantGates {
				t.Fatalf("Gates = %v, want %v (grade %+v)", g.Gates, tc.wantGates, g)
			}
			if g.Dial != tc.wantDial {
				t.Errorf("Dial = %q, want %q", g.Dial, tc.wantDial)
			}
			if tc.wantNote != "" && !strings.Contains(g.Note(), tc.wantNote) {
				t.Errorf("Note() = %q, want it to contain %q", g.Note(), tc.wantNote)
			}
			if g.Gates && (g.Note() != "" || g.OptIn() != "") {
				t.Errorf("a gating grade says %q / %q, want nothing", g.Note(), g.OptIn())
			}
			if !g.Gates && g.OptIn() != "lint.rules: {"+rolloutRule+": error}" {
				t.Errorf("OptIn() = %q", g.OptIn())
			}
		})
	}
}

// The real rows: both rules are new in the next release, so a project pinned
// at the current release (or the introducing one) is not blocked by them, and
// one pinned at GatesFrom is.
func TestVulnGateRulesWarnFirst(t *testing.T) {
	prevHistory := previousPinFromHistory
	t.Cleanup(func() { previousPinFromHistory = prevHistory })
	previousPinFromHistory = func(string, string) (string, bool) { return "", false }

	for _, rule := range []string{RuleVulnGate, RuleSBOMRequired} {
		ro, ok := lintRollouts[rule]
		if !ok || ro.GatesFrom == "" {
			t.Fatalf("%s has no warn-first rollout row: %+v", rule, ro)
		}
		if g := GradeRule(pinnedCfg("v1.8.0", nil), rule); g.Gates {
			t.Errorf("%s gates a project pinned at v1.8.0, which predates it", rule)
		}
		if g := GradeRule(pinnedCfg(ro.IntroducedIn, nil), rule); g.Gates {
			t.Errorf("%s gates a project pinned at %s, the release that introduces it", rule, ro.IntroducedIn)
		}
		if g := GradeRule(pinnedCfg(ro.GatesFrom, nil), rule); !g.Gates {
			t.Errorf("%s does not gate a project pinned at its GatesFrom %s", rule, ro.GatesFrom)
		}
	}
}

func TestRuleGradePredates(t *testing.T) {
	g := RuleGrade{Rule: rolloutRule, IntroducedIn: "v1.9.0", GatesFrom: "v1.9.1"}
	for v, want := range map[string]bool{
		"v1.8.4":                               true,
		"1.8.0":                                true,
		"v1.9.0":                               false,
		"v1.10.0":                              false,
		"v1.9.1-0.20261120000000-abcdef123456": false, // leads to v1.9.1
		"":                                     false,
		"(devel)":                              false,
		"dev":                                  false,
	} {
		if got := g.Predates(v); got != want {
			t.Errorf("Predates(%q) = %v, want %v", v, got, want)
		}
	}
}
