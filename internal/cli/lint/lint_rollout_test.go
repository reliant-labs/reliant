package lint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/config"
)

// A rule that joins forge lint, or is tightened, must not turn a consumer's
// gate red the day the consumer pins the forge that ships it. These tests pin
// the whole rollout primitive: the declaration every rule owes (lint_rollout.go),
// what the project's forge_version makes of it, how lint.rules overrides it,
// and what every output mode says about it.

// rolloutRule is a synthetic row, so the semantics do not depend on any real
// rule's dates.
const rolloutRule = "forge-closed-set"

var testRollouts = map[string]ruleRollout{
	rolloutRule: {IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Fix: "forge fix example"},
}

// withRollouts makes table the rollout declaration for the rest of the test,
// for a test that goes through an entry point reading lintRollouts. The real
// table may hold no rule in grace (newRules empty), so the semantics are
// pinned on a synthetic one.
func withRollouts(t *testing.T, table map[string]ruleRollout) {
	t.Helper()
	saved := lintRollouts
	lintRollouts = table
	t.Cleanup(func() { lintRollouts = saved })
}

func pinnedCfg(pin string, rules map[string]string) *config.ProjectConfig {
	return &config.ProjectConfig{ForgeVersion: pin, Lint: config.LintConfig{Rules: rules}}
}

func rolloutFinding(sev string) lintJSONFinding {
	return lintJSONFinding{
		File: "db/migrations/0001_x.up.sql", Line: 4, Severity: sev, Rule: rolloutRule,
		Message: "CHECK lists a value Go does not grant", FixHint: "regenerate the set",
	}
}

// A declaration is mandatory: every rule lint.rules can grade owns a row, and
// a row names a rule that exists. A new lane cannot ship without one.
func TestEveryGradableRuleDeclaresItsRollout(t *testing.T) {
	var missing []string
	for _, r := range gradableRules() {
		if _, ok := lintRollouts[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Errorf("lint rule(s) with no rollout declaration in lint_rollout_table.go: %s\n"+
			"Declare the release that introduced each rule; see lint_rollout_table.go for when a new rule can opt into grace.",
			strings.Join(missing, ", "))
	}

	gradable := gradableRuleSet()
	for r := range lintRollouts {
		if !gradable[r] {
			t.Errorf("rollout row %q names no rule lint.rules grades; a stale row, or a misspelling", r)
		}
	}
}

// Validate the declarations before projection hides the authoring marker
// or lets a later declaration overwrite a shipped rule.
func TestRolloutRowsAreWellFormed(t *testing.T) {
	for _, g := range shippedRules {
		if g.Grace != nil {
			t.Errorf("%v: shipped rules may not reopen a grace window", g.Rules)
		}
	}
	seen := make(map[string]bool)
	for _, groups := range [][]rolloutGroup{shippedRules, newRules} {
		for _, g := range groups {
			if err := validateRolloutGroup(g); err != nil {
				t.Errorf("%v: %v", g.Rules, err)
			}
			for _, rule := range g.Rules {
				if seen[rule] {
					t.Errorf("%s: duplicate rollout declaration", rule)
				}
				seen[rule] = true
			}
		}
	}
}

func validateRolloutGroup(g rolloutGroup) error {
	// Fixed to the approved NEXT_VERSION at the time of the decision. Reading
	// the live file here would reopen the exception for later introductions
	// and invalidate the v1.9.0 rows when the release moves NEXT_VERSION.
	const introduced, until = "v1.9.0", "v1.10.0"
	switch {
	case !semver.IsValid(g.IntroducedIn):
		return fmt.Errorf("introduced_in %q is not a vX.Y.Z version", g.IntroducedIn)
	case g.Grace == nil:
		if semver.Compare(g.IntroducedIn, introduced) > 0 {
			return fmt.Errorf("rollout for %s is beyond the approved %s cohort; ask the owner before declaring its policy", g.IntroducedIn, introduced)
		}
		return nil
	case g.IntroducedIn != introduced:
		return fmt.Errorf("Grace is only allowed for newly introduced %s rules, not %s", introduced, g.IntroducedIn)
	case strings.TrimSpace(g.Grace.Reason) == "":
		return fmt.Errorf("Grace requires a non-empty Reason")
	case g.Grace.Until != until:
		return fmt.Errorf("Grace.Until must be %s, got %q", until, g.Grace.Until)
	}
	return nil
}

// The link-time fixture is one grace row, "<rule>@<introduced_in>@<until>",
// and anything else is no row at all: a release build (empty) declares
// nothing, and no test binary links one.
func TestLinkedFixtureRows(t *testing.T) {
	got := linkedFixtureRows("forge-column-markers@v1.0.0@v1.0.1")
	if len(got) != 1 || got[0].IntroducedIn != "v1.0.0" || got[0].Grace == nil || got[0].Grace.Until != "v1.0.1" ||
		len(got[0].Rules) != 1 || got[0].Rules[0] != "forge-column-markers" {
		t.Fatalf("got %+v", got)
	}
	if ro := buildRollouts(shippedRules, got)["forge-column-markers"]; ro.IntroducedIn != "v1.0.0" || ro.GatesFrom != "v1.0.1" {
		t.Errorf("the linked row must override the shipped declaration, got %+v", ro)
	}
	for _, bad := range []string{"", "forge-column-markers", "forge-column-markers@v1.0.0", "@v1.0.0@v1.0.1",
		"forge-column-markers@@v1.0.1", "forge-column-markers@v1.0.0@", "a@b@c@d"} {
		if got := linkedFixtureRows(bad); got != nil {
			t.Errorf("%q: got %+v, want no row", bad, got)
		}
	}
	if rolloutLinkFixture != "" {
		t.Errorf("rolloutLinkFixture = %q in a test binary: only the e2e forge build links one", rolloutLinkFixture)
	}
}

func TestRolloutGraceRequiresExplicitV19OptIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  rolloutGroup
		want string
	}{
		{name: "old rule", row: rolloutGroup{IntroducedIn: "v1.8.0", Grace: &rolloutGrace{Until: "v1.10.0", Reason: "old"}}, want: "only allowed"},
		{name: "later rule", row: rolloutGroup{IntroducedIn: "v1.10.0", Grace: &rolloutGrace{Until: "v1.11.0", Reason: "later"}}, want: "only allowed"},
		{name: "later unmarked rule", row: rolloutGroup{IntroducedIn: "v1.10.0"}, want: "beyond the approved"},
		{name: "missing reason", row: rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.10.0", Reason: " \t"}}, want: "Reason"},
		{name: "missing deadline", row: rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Reason: "test"}}, want: "Grace.Until"},
		{name: "invalid deadline", row: rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "later", Reason: "test"}}, want: "Grace.Until"},
		{name: "same release", row: rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.9.0", Reason: "test"}}, want: "Grace.Until"},
		{name: "patch deadline", row: rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.9.1", Reason: "test"}}, want: "Grace.Until"},
		{name: "later deadline", row: rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.11.0", Reason: "test"}}, want: "Grace.Until"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateRolloutGroup(tc.row); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateRolloutGroup: got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestExplicitRolloutGraceWarnsUntilNextMinor(t *testing.T) {
	g := rolloutGroup{IntroducedIn: "v1.9.0", Grace: &rolloutGrace{Until: "v1.10.0", Reason: "New findings need time to fix"}, Rules: []string{rolloutRule}}
	if err := validateRolloutGroup(g); err != nil {
		t.Fatal(err)
	}
	table := buildRollouts([]rolloutGroup{g})
	for _, pin := range []string{"v1.9.0", "v1.9.1", "v1.10.0"} {
		got, gated := gradeProjectLaneWith(table, pinnedCfg(pin, nil), []lintJSONFinding{rolloutFinding(lintSevError)}, true)
		if pin == "v1.10.0" {
			if !gated || got[0].Severity != lintSevError || got[0].Grace != nil {
				t.Errorf("%s: want an error with no grace, got %+v gated=%t", pin, got, gated)
			}
		} else if gated || got[0].Severity != lintSevWarning || got[0].Grace == nil || got[0].Grace.GatesFrom != "v1.10.0" {
			t.Errorf("%s: want a warning until v1.10.0, got %+v gated=%t", pin, got, gated)
		}
	}
}

func TestClosedV19RolloutRowsStayClosed(t *testing.T) {
	for _, rule := range []string{ruleUnprovenSignupDefault, "forgeconv-one-rpc-per-file"} {
		if ro := lintRollouts[rule]; ro.IntroducedIn != "v1.9.0" || ro.GatesFrom != "" {
			t.Errorf("%s: #2410 closed this window, got %+v", rule, ro)
		}
	}
}

// A row is for a release that shipped (at or before VERSION), for the minor
// NEXT_VERSION names, or for a patch release between them: the version is
// computed (scripts/semver-next.sh), so a fix-only release can ship before
// the next minor, and one already cut (an RC set in flight) carries its own
// rows. A row past NEXT_VERSION dates its rule to a release that may never
// ship, and the rule would gate early or never.
func TestRolloutRowsNameTheNextRelease(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		v := strings.TrimSpace(string(b))
		if !semver.IsValid(v) {
			t.Fatalf("%s holds %q, not a vX.Y.Z version", name, v)
		}
		return v
	}
	last, next := read("VERSION"), read("NEXT_VERSION")
	if semver.Compare(next, last) <= 0 {
		t.Fatalf("NEXT_VERSION %s must be later than VERSION %s", next, last)
	}
	for rule, ro := range lintRollouts {
		if semver.Compare(ro.IntroducedIn, next) > 0 {
			t.Errorf("%s: introduced_in %s is past the next feature release (VERSION is %s, "+
				"NEXT_VERSION is %s)", rule, ro.IntroducedIn, last, next)
		}
	}
}

// The version a release gets is computed (scripts/semver-next.sh): a fix-only
// range is VERSION's next patch, a feat the minor NEXT_VERSION names. A rule
// keyed to NEXT_VERSION ships in whichever comes first, so a consumer pinned
// to either still sees its findings as warnings. A rule keyed to a patch
// release before NEXT_VERSION (one an RC set already carries) warns at that
// release and gates from the one after it, as every rollout row does.
func TestFirstComputedReleaseCarryingARuleWarns(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return strings.TrimSpace(string(b))
	}
	last, next := read("VERSION"), read("NEXT_VERSION")
	if !strings.HasSuffix(next, ".0") {
		t.Fatalf("NEXT_VERSION %s must be a minor (vX.Y.0): a feat release is computed as the next minor", next)
	}
	var maj, min, patch int
	if _, err := fmt.Sscanf(last, "v%d.%d.%d", &maj, &min, &patch); err != nil {
		t.Fatalf("VERSION %q: %v", last, err)
	}
	for rule, ro := range lintRollouts {
		if ro.GatesFrom == "" || semver.Compare(ro.IntroducedIn, last) <= 0 {
			continue
		}
		firstReleases := []string{ro.IntroducedIn}
		if ro.IntroducedIn == next {
			firstReleases = append(firstReleases, fmt.Sprintf("v%d.%d.%d", maj, min, patch+1))
		}
		for _, pin := range firstReleases {
			f := lintJSONFinding{File: "x", Line: 1, Severity: lintSevError, Rule: rule, Message: "m"}
			got, gated := gradeProjectLaneWith(map[string]ruleRollout{rule: ro}, pinnedCfg(pin, nil), []lintJSONFinding{f}, true)
			if len(got) != 1 || got[0].Severity != lintSevWarning || gated {
				t.Errorf("%s (introduced %s, gates from %s): a project pinned to %s, a first release carrying it, "+
					"must see a warning; got %+v gated=%v", rule, ro.IntroducedIn, ro.GatesFrom, pin, got, gated)
			}
		}
	}
}

// The retro-tagged lanes carry the release that really introduced them.
func TestRetroTaggedLanesCarryTheirRealVersion(t *testing.T) {
	for rule, want := range map[string]string{
		"static-export-dynamic-segment": "v0.1.44", // lint --static-export, #cf5f13047
		"enum-value-used-before-commit": "v0.1.45",
		"identity-share-with":           "v0.1.46",
		"forgeconv-no-getenv":           "v1.0.0",
		"forge-closed-set":              "v1.0.0",
		"forge-graph-unreachable":       "v1.0.0",
		"forge-column-markers":          "v1.0.0", // existed earlier; tightened from warn to gate
		"forge-db-grants-undecided":     "v1.0.0",
		"secrets-cluster-kind":          "v1.0.0",
		"forgeconv-activity-declared":   "v1.0.0",
		rolloutGolangciBuildTags:        "v1.0.0", // #1205 / #1224
	} {
		ro, ok := lintRollouts[rule]
		if !ok {
			t.Errorf("%s has no rollout row", rule)
			continue
		}
		if ro.IntroducedIn != want {
			t.Errorf("%s introduced_in = %s, want %s", rule, ro.IntroducedIn, want)
		}
	}
}

func TestPinDecidesWhetherARuleStillWarns(t *testing.T) {
	for name, tc := range map[string]struct {
		pin       string
		rules     map[string]string
		wantSev   string
		wantGrace bool
		wantGated bool
	}{
		"a pin from before the release that ships the rule warns": {
			pin: "v0.1.46", wantSev: lintSevWarning, wantGrace: true,
		},
		"a pseudo-version of the shipping release warns (it sorts below the release)": {
			pin: "v0.1.47-0.20261022211527-ce1870c66476", wantSev: lintSevWarning, wantGrace: true,
		},
		"the shipping release itself still warns": {
			pin: "v1.0.0", wantSev: lintSevWarning, wantGrace: true,
		},
		"a dirty pin compares as its base version": {
			pin: "v1.0.0+dirty", wantSev: lintSevWarning, wantGrace: true,
		},
		"a pin without the leading v compares too": {
			pin: "0.1.46", wantSev: lintSevWarning, wantGrace: true,
		},
		// forge#2230: a pseudo-version reads as the release it leads to, so
		// it grades exactly as that commit's tag would.
		"a pseudo-version leading to gates_from gates, as its tag does": {
			pin: "v1.0.1-0.20261030000000-aaaaaaaaaaaa", wantSev: lintSevError, wantGated: true,
		},
		"a dirty pseudo-version leading to gates_from gates": {
			pin: "v1.0.1-0.20261030000000-aaaaaaaaaaaa+dirty", wantSev: lintSevError, wantGated: true,
		},
		"a pseudo-version leading to the shipping release warns, as its tag does": {
			pin: "v1.0.0-0.20261030000000-aaaaaaaaaaaa", wantSev: lintSevWarning, wantGrace: true,
		},
		"a pseudo-version after a pre-release reads as that release": {
			pin: "v1.0.1-rc.1.0.20261030000000-aaaaaaaaaaaa", wantSev: lintSevError, wantGated: true,
		},
		"a pseudo-version with no tag behind it sorts below every release": {
			pin: "v0.0.0-20261030000000-aaaaaaaaaaaa", wantSev: lintSevWarning, wantGrace: true,
		},
		"the gates_from release gates": {
			pin: "v1.0.1", wantSev: lintSevError, wantGated: true,
		},
		"a later release gates": {
			pin: "v1.1.0", wantSev: lintSevError, wantGated: true,
		},
		"a pseudo-version past gates_from gates": {
			pin: "v1.0.2-0.20261101000000-bbbbbbbbbbbb", wantSev: lintSevError, wantGated: true,
		},
		"no pin gates: nothing says the project is behind": {
			pin: "", wantSev: lintSevError, wantGated: true,
		},
		"forge's own first-user sentinel gates": {
			pin: "0.0.0", wantSev: lintSevError, wantGated: true,
		},
		"a pin that is not a version gates": {
			pin: "(devel)", wantSev: lintSevError, wantGated: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := pinnedCfg(tc.pin, nil)
			got, gated := gradeProjectLaneWith(testRollouts, cfg, []lintJSONFinding{rolloutFinding(lintSevError)}, true)
			if len(got) != 1 {
				t.Fatalf("findings = %+v", got)
			}
			if got[0].Severity != tc.wantSev || gated != tc.wantGated {
				t.Errorf("severity=%s gated=%v, want %s / %v", got[0].Severity, gated, tc.wantSev, tc.wantGated)
			}
			if (got[0].Grace != nil) != tc.wantGrace {
				t.Errorf("grace = %+v, want present=%v", got[0].Grace, tc.wantGrace)
			}
			if tc.wantGrace && (got[0].Grace.IntroducedIn != "v1.0.0" || got[0].Grace.GatesFrom != "v1.0.1" ||
				got[0].Grace.Fix != "forge fix example") {
				t.Errorf("grace note = %+v", got[0].Grace)
			}
		})
	}
}

// A pin bump names the rules whose grace it ends (forge#2230), so a red gate
// is never the first a project hears of them. A pseudo-version and its tag
// are the same place, so moving from one to the other ends nothing.
func TestRulesStartingToGateAcrossAPinBump(t *testing.T) {
	for name, tc := range map[string]struct {
		from, to string
		rules    map[string]string
		want     bool
	}{
		"crossing gates_from ends the grace":               {from: "v1.0.0", to: "v1.0.1", want: true},
		"a later release from the shipping one crosses it": {from: "v1.0.0", to: "v1.2.0", want: true},
		// forge#2282: a pin that jumps the whole window warns at the new
		// pin, so this bump ends nothing; the next one does.
		"a jump over the whole window ends nothing yet":     {from: "v0.1.46", to: "v1.2.0"},
		"a pseudo-version leading to gates_from crosses it": {from: "v1.0.0", to: "v1.0.1-0.20261030000000-aaaaaaaaaaaa", want: true},
		"a pseudo-version to its own tag crosses nothing":   {from: "v1.0.1-0.20261030000000-aaaaaaaaaaaa", to: "v1.0.1"},
		"a bump that stays before gates_from ends nothing":  {from: "v0.1.46", to: "v1.0.0"},
		"a bump that starts past gates_from ends nothing":   {from: "v1.0.1", to: "v1.1.0"},
		"an unpinned project already gates everything":      {from: "", to: "v1.0.1"},
		"a dev target does not move the pin":                {from: "v1.0.0", to: "(devel)"},
		"lint.rules naming the rule decides it instead":     {from: "v1.0.0", to: "v1.0.1", rules: map[string]string{rolloutRule: "warn"}},
		"lint.rules `*` decides it instead":                 {from: "v1.0.0", to: "v1.0.1", rules: map[string]string{"*": "error"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := rulesStartingToGate(testRollouts, pinnedCfg(tc.from, tc.rules), tc.from, tc.to)
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			want := GatingRule{Rule: rolloutRule, IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1", Fix: "forge fix example"}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("got %+v, want [%+v]", got, want)
			}
		})
	}
	// The bump after a jump ends the jump's warn pin.
	withPreviousPin(t, "v0.1.46")
	if got := rulesStartingToGate(testRollouts, pinnedCfg("v1.2.0", nil), "v1.2.0", "v1.3.0"); len(got) != 1 || got[0].Rule != rolloutRule {
		t.Errorf("the pin after a jump must report the jumped rule starting to gate, got %+v", got)
	}
	// Upgrade-report defect 4: two consecutive bumps between pseudo-versions
	// that grade as one release. lint under the second pin still reads the
	// jump's previous pin from history (it skips same-graded pins), so the
	// rule still warns there: that bump ends nothing. The bump that leaves
	// the release ends it — once.
	for _, step := range []struct {
		from, to string
		want     bool
	}{
		{"v1.2.1-0.20261030000000-aaaaaaaaaaaa", "v1.2.1-0.20261031000000-bbbbbbbbbbbb", false},
		{"v1.2.1-0.20261031000000-bbbbbbbbbbbb", "v1.3.1-0.20261101000000-cccccccccccc", true},
	} {
		got := rulesStartingToGate(testRollouts, pinnedCfg(step.from, nil), step.from, step.to)
		if (len(got) == 1) != step.want || len(got) > 1 {
			t.Errorf("%s → %s: got %+v, want starts-gating=%v", step.from, step.to, got, step.want)
		}
		// The same answer lint gives under each pin, so the two cannot disagree.
		before, _ := gradeProjectLaneWith(testRollouts, pinnedCfg(step.from, nil), []lintJSONFinding{rolloutFinding(lintSevError)}, true)
		if before[0].Grace == nil {
			t.Errorf("lint under %s should warn (the jump grace)", step.from)
		}
	}
	if got := rulesStartingToGate(testRollouts, &config.ProjectConfig{ModulePath: "github.com/reliant-labs/forge"}, "v1.0.0", "v1.0.1"); len(got) != 0 {
		t.Errorf("forge's own tree builds the forge it lints with; got %+v", got)
	}
}

// withPreviousPin stands in for the git history lookup: the project came
// from prev ("" = history cannot say).
func withPreviousPin(t *testing.T, prev string) {
	t.Helper()
	saved := previousPinFromHistory
	t.Cleanup(func() { previousPinFromHistory = saved })
	previousPinFromHistory = func(string, string) (string, bool) {
		if prev == "" {
			return "", false
		}
		return rolloutPin(prev)
	}
}

// forge#2282: a project whose pin jumps past a rule's whole warn-first window
// (introduced and gating after its previous pin) gets one pin of warnings, by
// the previous forge_version git history shows, instead of gating on arrival.
// A project that moved one release at a time grades exactly as before.
func TestPreviousPinGivesAJumpedRuleItsWarnPin(t *testing.T) {
	for name, tc := range map[string]struct {
		pin, prev string
		wantGrace bool
		wantAfter string // the pin the jump grace names; "" for the plain grace
	}{
		"no history: the pin alone decides, as before":     {pin: "v1.2.0"},
		"came from before the rule: warns at this pin":     {pin: "v1.2.0", prev: "v0.1.46", wantGrace: true, wantAfter: "v1.2.0"},
		"came from the shipping release: gates, as before": {pin: "v1.0.1", prev: "v1.0.0"},
		"came from past the rule: gates":                   {pin: "v1.2.0", prev: "v1.0.1"},
		"a pseudo-version pin keeps its spelling in the note": {
			pin: "v1.2.1-0.20261030000000-aaaaaaaaaaaa", prev: "v0.1.46", wantGrace: true, wantAfter: "v1.2.1-0.20261030000000-aaaaaaaaaaaa",
		},
		"before gates_from the plain grace still applies": {pin: "v1.0.0", prev: "v0.1.40", wantGrace: true},
	} {
		t.Run(name, func(t *testing.T) {
			withPreviousPin(t, tc.prev)
			got, gated := gradeProjectLaneWith(testRollouts, pinnedCfg(tc.pin, nil), []lintJSONFinding{rolloutFinding(lintSevError)}, true)
			if (got[0].Grace != nil) != tc.wantGrace || gated == tc.wantGrace {
				t.Fatalf("grace=%+v gated=%v, want grace=%v", got[0].Grace, gated, tc.wantGrace)
			}
			if tc.wantGrace && got[0].Grace.GatesAfter != tc.wantAfter {
				t.Errorf("gates_after = %q, want %q", got[0].Grace.GatesAfter, tc.wantAfter)
			}
		})
	}

	// The note says why the rule warns past its gates_from, and when it gates.
	withPreviousPin(t, "v0.1.46")
	cfg := pinnedCfg("v1.2.0", nil)
	got, _ := gradeProjectLaneWith(testRollouts, cfg, []lintJSONFinding{rolloutFinding(lintSevError)}, true)
	note := got[0].Grace.note(rolloutRule)
	for _, want := range []string{"new in v1.0.0", "previous forge pin v0.1.46", "warns at v1.2.0", "gates from the next pin", "{" + rolloutRule + ": error}"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not say %q", note, want)
		}
	}
	if verdict := describeGraced(gracedFindings([]lintStepOutcome{{ran: true, findings: got}})); !strings.Contains(verdict, "gates from the next forge pin after v1.2.0") {
		t.Errorf("verdict clause %q does not say when the rule gates", verdict)
	}
	// lint.rules still wins over history.
	cfg.Lint.Rules = map[string]string{rolloutRule: "error"}
	if _, gated := gradeProjectLaneWith(testRollouts, cfg, []lintJSONFinding{rolloutFinding(lintSevError)}, true); !gated {
		t.Error("lint.rules <rule>: error must gate whatever the previous pin was")
	}
}

// writePinRepo makes a git repository whose forge.yaml moved through pins,
// one commit each, and returns its directory.
func writePinRepo(t *testing.T, pins ...string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for i, pin := range pins {
		body := fmt.Sprintf("# manifest\nname: app\nmodule_path: example.com/app\nforge_version: %s # pinned\n", pin)
		if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		// An unrelated forge.yaml edit between pins is not a pin move.
		if i == 1 {
			run("add", "forge.yaml")
			run("commit", "-q", "-m", fmt.Sprintf("pin %s", pin))
			if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(body+"lint:\n  rules: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		run("add", "forge.yaml")
		run("commit", "-q", "--allow-empty", "-m", fmt.Sprintf("forge.yaml at %s", pin))
	}
	return dir
}

// The previous pin, read from git history: the forge_version before the
// newest change to it, through an unrelated forge.yaml edit, and a pin moved
// but not yet committed. No git, or a clone too shallow to show the change,
// says nothing — the pin alone decides.
func TestGitPreviousPin(t *testing.T) {
	if testing.Short() {
		t.Skip("execs git; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := writePinRepo(t, "v0.1.46", "v1.0.0", "v1.7.0")
	if prev, ok := gitPreviousPin(repo, "v1.7.0"); !ok || prev != "v1.0.0" {
		t.Errorf("previous pin = %q, %v; want v1.0.0", prev, ok)
	}

	// The pin jump through the real lookup: a rule introduced after v1.0.0
	// warns at v1.7.0.
	cfg, err := config.LoadProject(mustRead(t, filepath.Join(repo, "forge.yaml")), filepath.Join(repo, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	jump := map[string]ruleRollout{rolloutRule: {IntroducedIn: "v1.5.0", GatesFrom: "v1.5.1"}}
	got, gated := gradeProjectLaneWith(jump, cfg, []lintJSONFinding{rolloutFinding(lintSevError)}, true)
	if gated || got[0].Grace == nil || got[0].Grace.PreviousPin != "v1.0.0" {
		t.Errorf("a v1.0.0 -> v1.7.0 jump gated a rule introduced in v1.5.0: grace=%+v gated=%v", got[0].Grace, gated)
	}

	// Lint wrote nothing, and forge.yaml carries no key an older v1 forge's
	// strict loader would refuse.
	before := mustRead(t, filepath.Join(repo, "forge.yaml"))
	if _, err := config.LoadProject(before, filepath.Join(repo, "forge.yaml")); err != nil {
		t.Errorf("forge.yaml no longer loads strictly: %v", err)
	}
	if strings.Contains(string(before), "rollout") {
		t.Errorf("forge.yaml gained rollout state:\n%s", before)
	}

	// A pin moved in the working tree, not committed: HEAD's is previous.
	body := strings.Replace(string(before), "v1.7.0", "v1.8.0", 1)
	if err := os.WriteFile(filepath.Join(repo, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if prev, ok := gitPreviousPin(repo, "v1.8.0"); !ok || prev != "v1.7.0" {
		t.Errorf("uncommitted bump: previous pin = %q, %v; want v1.7.0", prev, ok)
	}

	// No git at all.
	if _, ok := gitPreviousPin(t.TempDir(), "v1.7.0"); ok {
		t.Error("a directory with no git history named a previous pin")
	}
	// A shallow clone that stops at the pin bump.
	shallow := filepath.Join(t.TempDir(), "shallow")
	if out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+repo, shallow).CombinedOutput(); err != nil {
		t.Fatalf("shallow clone: %v\n%s", err, out)
	}
	if prev, ok := gitPreviousPin(shallow, "v1.7.0"); ok {
		t.Errorf("a depth-1 clone named previous pin %q; it cannot see one", prev)
	}
	// ... and grades by the pin alone, as before.
	scfg, err := config.LoadProject(mustRead(t, filepath.Join(shallow, "forge.yaml")), filepath.Join(shallow, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, gated := gradeProjectLaneWith(jump, scfg, []lintJSONFinding{rolloutFinding(lintSevError)}, true); !gated {
		t.Error("with no history the pin alone decides: v1.7.0 is past gates_from v1.5.1")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// lint.rules decides severity when it names the rule; the rollout only
// supplies the default. So a project opts into gating early with `error`,
// and extends the grace period for as long as it likes with `warn`.
func TestLintRulesOverrideTheRollout(t *testing.T) {
	const old = "v0.1.46" // behind: the rule is in grace
	for name, tc := range map[string]struct {
		rules     map[string]string
		wantSev   string // "" = the finding is dropped
		wantGrace bool
		wantGated bool
	}{
		"rule: error opts in early": {
			rules: map[string]string{rolloutRule: "error"}, wantSev: lintSevError, wantGated: true,
		},
		"rule: warn is an ordinary dial, not the rollout's": {
			rules: map[string]string{rolloutRule: "warn"}, wantSev: lintSevWarning,
		},
		"rule: off drops it": {
			rules: map[string]string{rolloutRule: "off"},
		},
		"* : error opts every rule in": {
			rules: map[string]string{"*": "error"}, wantSev: lintSevError, wantGated: true,
		},
		"* : warn is an ordinary dial": {
			rules: map[string]string{"*": "warn"}, wantSev: lintSevWarning,
		},
		"another rule's dial leaves the rollout alone": {
			rules: map[string]string{"forge-graph-unreachable": "error"}, wantSev: lintSevWarning, wantGrace: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, gated := gradeProjectLaneWith(testRollouts, pinnedCfg(old, tc.rules),
				[]lintJSONFinding{rolloutFinding(lintSevError)}, true)
			if tc.wantSev == "" {
				if len(got) != 0 || gated {
					t.Fatalf("off must drop the finding and its verdict: %+v gated=%v", got, gated)
				}
				return
			}
			if len(got) != 1 || got[0].Severity != tc.wantSev || gated != tc.wantGated {
				t.Fatalf("got %+v gated=%v, want %s gated=%v", got, gated, tc.wantSev, tc.wantGated)
			}
			if (got[0].Grace != nil) != tc.wantGrace {
				t.Errorf("grace = %+v, want present=%v", got[0].Grace, tc.wantGrace)
			}
		})
	}
}

// Grace changes the DEFAULT severity of a finding that would have failed the
// gate. It never touches a warning, another tool's findings, or a lane that
// failed for a reason of its own.
func TestGraceTouchesOnlyWhatWouldHaveGated(t *testing.T) {
	cfg := pinnedCfg("v0.1.46", nil)

	t.Run("a warning is left alone and carries no note", func(t *testing.T) {
		got, _ := gradeProjectLaneWith(testRollouts, cfg, []lintJSONFinding{rolloutFinding(lintSevWarning)}, false)
		if got[0].Severity != lintSevWarning || got[0].Grace != nil {
			t.Errorf("a warning gained a grace note: %+v", got[0])
		}
	})
	t.Run("a legacy rule keeps gating", func(t *testing.T) {
		f := lintJSONFinding{Severity: lintSevError, Rule: "forgeconv-handler-file-size", File: "a.go", Message: "m"}
		got, gated := gradeProjectLaneWith(testRollouts, cfg, []lintJSONFinding{f}, true)
		if got[0].Severity != lintSevError || !gated || got[0].Grace != nil {
			t.Errorf("got %+v gated=%v", got[0], gated)
		}
	})
	t.Run("a real error in the same lane still gates it", func(t *testing.T) {
		real := lintJSONFinding{Severity: lintSevError, Rule: "forgeconv-handler-file-size", File: "a.go", Message: "m"}
		got, gated := gradeProjectLaneWith(testRollouts, cfg,
			[]lintJSONFinding{rolloutFinding(lintSevError), real}, true)
		if !gated {
			t.Error("the lane lost its gate over a graced neighbour")
		}
		if got[0].Severity != lintSevWarning || got[1].Severity != lintSevError {
			t.Errorf("severities = %s, %s", got[0].Severity, got[1].Severity)
		}
	})
	t.Run("a lane that failed without a finding still fails", func(t *testing.T) {
		_, gated := gradeProjectLaneWith(testRollouts, cfg, nil, true)
		if !gated {
			t.Error("a lane gated with no findings must stay gated")
		}
	})
	t.Run("no config: nothing is behind", func(t *testing.T) {
		got, gated := gradeProjectLaneWith(testRollouts, nil, []lintJSONFinding{rolloutFinding(lintSevError)}, true)
		if got[0].Severity != lintSevError || !gated {
			t.Errorf("got %+v gated=%v", got[0], gated)
		}
	})
}

// The golangci-lint build-tag passes (#1205, #1224) have no rule ID of their
// own — their findings are noctx, errcheck, … — so they declare the rollout
// from the pass, and lint.rules names it by the pass's key.
func TestBuildTagPassFindingsRollOutByPass(t *testing.T) {
	table := map[string]ruleRollout{rolloutGolangciBuildTags: {IntroducedIn: "v1.0.0", GatesFrom: "v1.0.1"}}
	tagged := tagRollout([]lintJSONFinding{
		{File: "a/a_integration_test.go", Line: 7, Severity: lintSevError, Rule: "noctx", Message: "must not be called"},
		{Severity: lintSevError, Rule: "external", Message: "net/http.Get(url)"}, // golangci's source snippet line
	}, rolloutGolangciBuildTags)
	untagged := lintJSONFinding{File: "a/a.go", Line: 1, Severity: lintSevError, Rule: "noctx", Message: "default pass"}
	all := append([]lintJSONFinding{untagged}, tagged...)

	got, gated := gradeProjectLaneWith(table, pinnedCfg("v0.1.46", nil), all, true)
	if !gated || got[0].Severity != lintSevError || got[0].Grace != nil {
		t.Fatalf("the default pass is not new and must keep gating: %+v gated=%v", got[0], gated)
	}
	for _, f := range got[1:] {
		if f.Severity != lintSevWarning || f.Grace == nil || f.Grace.GatesFrom != "v1.0.1" {
			t.Errorf("a tagged-pass finding did not roll out: %+v", f)
		}
	}

	onlyTagged := all[1:]
	if _, gated := gradeProjectLaneWith(table, pinnedCfg("v0.1.46", nil), onlyTagged, true); gated {
		t.Error("a lane whose only errors came from the new pass gated inside the grace period")
	}
	if _, gated := gradeProjectLaneWith(table, pinnedCfg("v1.0.1", nil), onlyTagged, true); !gated {
		t.Error("the new pass did not gate from its gates_from release")
	}
	if _, gated := gradeProjectLaneWith(table, pinnedCfg("v0.1.46", map[string]string{rolloutGolangciBuildTags: "error"}), onlyTagged, true); !gated {
		t.Error("lint.rules golangci-build-tags: error did not opt in early")
	}
	// `*` never grades another tool's findings; the pass's own key does.
	if got, gated := gradeProjectLaneWith(table, pinnedCfg("v0.1.46", map[string]string{"*": "error"}), onlyTagged, true); gated || got[0].Severity != lintSevWarning {
		t.Errorf("* must not raise another tool's findings: %+v gated=%v", got, gated)
	}
	if got, gated := gradeProjectLaneWith(table, pinnedCfg("v0.1.46", map[string]string{rolloutGolangciBuildTags: "off"}), onlyTagged, true); len(got) != 0 || gated {
		t.Errorf("lint.rules golangci-build-tags: off must drop the pass's findings: %+v", got)
	}
}

// Merging the build-tag passes dedupes by finding; tagging a finding must not
// stop a default-pass finding from absorbing its tagged twin.
func TestMergedBuildTagFindingsStillDedupe(t *testing.T) {
	base := []lintJSONFinding{{File: "a/a.go", Line: 3, Severity: lintSevError, Rule: "noctx", Message: "m"}}
	more := []lintJSONFinding{
		{File: "a/a.go", Line: 3, Severity: lintSevError, Rule: "noctx", Message: "m"},
		{File: "a/a_integration_test.go", Line: 7, Severity: lintSevError, Rule: "noctx", Message: "m2"},
	}
	got := mergeRolledOutFindings(base, more, rolloutGolangciBuildTags)
	if len(got) != 2 {
		t.Fatalf("want the base finding plus the one new finding, got %+v", got)
	}
	if got[0].rollout != "" || got[1].rollout != rolloutGolangciBuildTags {
		t.Errorf("only the finding the extra pass added is tagged: %+v", got)
	}
}

// Through the real engine: the verdict a user reads.
func TestRolloutVerdictThroughTheEngine(t *testing.T) {
	withRollouts(t, testRollouts)
	ro := testRollouts[rolloutRule]
	step := linterStep{
		name: "closed-sets lint", gates: true, scope: scopeByFile,
		shouldRun: func(*lintRunCtx) (bool, string) { return true, "" },
		collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
			return []lintJSONFinding{rolloutFinding(lintSevError), rolloutFinding(lintSevError)}, true, nil
		},
	}
	run := func(cfg *config.ProjectConfig, quiet bool) (string, []lintStepOutcome, error) {
		outcomes := collectLintOutcomesFrom(&lintRunCtx{ctx: t.Context(), cfg: cfg}, []linterStep{step, cleanGatingStep("contract linter")})
		var out bytes.Buffer
		err := renderLintOutcomes(&out, outcomes, quiet, nil)
		return out.String(), outcomes, err
	}

	// Behind: a warning, not a red gate, and the last line says what is coming.
	for _, quiet := range []bool{false, true} {
		out, outcomes, err := run(pinnedCfg("v0.1.46", nil), quiet)
		if err != nil {
			t.Fatalf("quiet=%v: a project behind the rule's gates_from failed its gate: %v\n%s", quiet, err, out)
		}
		if outcomes[0].gated {
			t.Errorf("quiet=%v: lane gated inside the grace period", quiet)
		}
		last := lastLine(out)
		for _, want := range []string{"✅", "2 new-rule findings", rolloutRule + " ×2", ro.GatesFrom} {
			if !strings.Contains(last, want) {
				t.Errorf("quiet=%v: the verdict line %q does not say %q", quiet, last, want)
			}
		}
		if !quiet {
			for _, want := range []string{
				"warn: [" + rolloutRule + "] is new in " + ro.IntroducedIn + " and gates from " + ro.GatesFrom,
				"lint.rules: {" + rolloutRule + ": error}",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("the lane's note does not say %q:\n%s", want, out)
				}
			}
		}
	}

	// At gates_from: the same findings fail, and the failure names the lane.
	if _, _, err := run(pinnedCfg(ro.GatesFrom, nil), false); err == nil || !strings.Contains(err.Error(), "closed-sets lint") {
		t.Errorf("at gates_from the lane must fail the run naming it, got %v", err)
	}
	// Early opt-in beats the pin.
	if _, _, err := run(pinnedCfg("v0.1.46", map[string]string{rolloutRule: "error"}), false); err == nil {
		t.Error("lint.rules <rule>: error did not gate a project still in the grace period")
	}
}

// --json carries the grace note as data, the run is ok, and the finding is a
// warning — a consumer's bot reads "what gates next" without parsing prose.
func TestGraceIsMachineReadableInJSON(t *testing.T) {
	got, gated := gradeProjectLaneWith(testRollouts, pinnedCfg("v0.1.46", nil), []lintJSONFinding{rolloutFinding(lintSevError)}, true)
	report := buildLintJSONReport(got, gated)
	if !report.OK || report.Summary.Errors != 0 || report.Summary.Warnings != 1 {
		t.Fatalf("report = %+v", report.Summary)
	}
	raw, err := json.Marshal(report.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	grace, _ := m["grace"].(map[string]any)
	if grace["introduced_in"] != "v1.0.0" || grace["gates_from"] != "v1.0.1" || grace["fix"] != "forge fix example" {
		t.Errorf("grace = %v in %s", grace, raw)
	}
	if _, leaked := m["rollout"]; leaked {
		t.Errorf("the internal rollout key leaked into the JSON: %s", raw)
	}
	// A finding that is not in grace has no grace key at all.
	plain, _ := json.Marshal(lintJSONFinding{Severity: lintSevError, Rule: "x", Message: "m"})
	if strings.Contains(string(plain), "grace") {
		t.Errorf("a plain finding grew a grace key: %s", plain)
	}
}

// The mechanism is in the one place every mode grades: the same source files
// must not call gradeLane for a verdict without going through it.
func TestOutcomeEngineGradesThroughTheRollout(t *testing.T) {
	for _, file := range []string{"lint_structured.go"} {
		src, err := os.ReadFile(filepath.Join(".", file))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "gradeLane(effectiveRules(") {
			t.Errorf("%s grades a lane with lint.rules alone; use gradeProjectLane so the rollout applies in every mode", file)
		}
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

// Upgrade-report defect 1: the proto-format lane reports each file as a
// finding plus its diff as a raw sub-tool line under it, and the verdict
// counted both: "60 new-rule findings" for 30 files. The raw line is the
// finding's output, not a finding.
func TestGracedFindingsCountRawToolLinesOnce(t *testing.T) {
	grace := &ruleGrace{IntroducedIn: "v1.8.0", GatesFrom: "v1.8.1"}
	fs := []lintJSONFinding{
		{File: "a.proto", Rule: "forge-proto-format", Severity: lintSevWarning, Grace: grace, rollout: "forge-proto-format"},
		{Rule: "external", Severity: lintSevWarning, Message: "--- a.proto\n+++ a.proto", Grace: grace, rollout: "forge-proto-format"},
		{File: "b.proto", Rule: "forge-proto-format", Severity: lintSevWarning, Grace: grace, rollout: "forge-proto-format"},
		{Rule: "external", Severity: lintSevWarning, Message: "--- b.proto", Grace: grace, rollout: "forge-proto-format"},
	}
	got := gracedFindings([]lintStepOutcome{{ran: true, findings: fs}})
	if len(got) != 1 || got[0].n != 2 || got[0].rule != "forge-proto-format" {
		t.Fatalf("got %+v, want forge-proto-format ×2", got)
	}
	if v := describeGraced(got); !strings.Contains(v, "2 new-rule findings") || !strings.Contains(v, "gates from v1.8.1") {
		t.Errorf("verdict clause %q", v)
	}
}

func TestGatesWhenIsOneWording(t *testing.T) {
	for _, tc := range []struct{ from, after, want string }{
		{"v1.8.1", "", "gates from v1.8.1"},
		{"v1.8.1", "v1.8.1-0.20261119173027-ff5a370de464", "gates from the next forge pin after v1.8.1-0.20261119173027-ff5a370de464"},
	} {
		if got := GatesWhen(tc.from, tc.after); got != tc.want {
			t.Errorf("GatesWhen(%q, %q) = %q, want %q", tc.from, tc.after, got, tc.want)
		}
	}
}
