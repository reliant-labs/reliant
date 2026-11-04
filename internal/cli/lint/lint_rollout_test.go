package lint

import (
	"bytes"
	"encoding/json"
	"os"
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
			"A rule that joins `forge lint` (or is tightened) declares the forge release that introduced it "+
			"and the next release it gates from, so a project that pins it sees warnings for one release "+
			"instead of a red gate: add `newRule(%q, ...)` — see lint_rollout.go.",
			strings.Join(missing, ", "), missing[0])
	}

	gradable := gradableRuleSet()
	for r := range lintRollouts {
		if !gradable[r] {
			t.Errorf("rollout row %q names no rule lint.rules grades; a stale row, or a misspelling", r)
		}
	}
}

// Rows are well formed: real releases, and a rule introduced after the
// baseline gates strictly later than it was introduced.
func TestRolloutRowsAreWellFormed(t *testing.T) {
	for rule, ro := range lintRollouts {
		if !semver.IsValid(ro.IntroducedIn) {
			t.Errorf("%s: introduced_in %q is not a vX.Y.Z version", rule, ro.IntroducedIn)
			continue
		}
		afterBaseline := semver.Compare(ro.IntroducedIn, rolloutBaseline) > 0
		switch {
		case afterBaseline && ro.GatesFrom == "":
			t.Errorf("%s: introduced in %s, after the baseline %s, so it must declare gates_from "+
				"(the release after the one that ships it)", rule, ro.IntroducedIn, rolloutBaseline)
		case afterBaseline && !semver.IsValid(ro.GatesFrom):
			t.Errorf("%s: gates_from %q is not a vX.Y.Z version", rule, ro.GatesFrom)
		case afterBaseline && semver.Compare(ro.GatesFrom, ro.IntroducedIn) <= 0:
			t.Errorf("%s: gates_from %s must be later than introduced_in %s — a rule warns for at least "+
				"one release before it gates", rule, ro.GatesFrom, ro.IntroducedIn)
		case !afterBaseline && ro.GatesFrom != "":
			t.Errorf("%s: introduced in %s, at or before the baseline %s, so it has gated since it shipped; "+
				"drop gates_from", rule, ro.IntroducedIn, rolloutBaseline)
		}
	}
}

// A row is for a release that shipped (at or before VERSION) or for the one
// NEXT_VERSION names. The next release is not always VERSION's patch+1
// (v0.1.46 was followed by v1.0.0), so a row written by guessing it would
// date its rule to a release that never ships, and the rule would gate early
// or never.
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
		if semver.Compare(ro.IntroducedIn, last) > 0 && ro.IntroducedIn != next {
			t.Errorf("%s: introduced_in %s is neither shipped (VERSION is %s) nor the next release "+
				"(NEXT_VERSION is %s)", rule, ro.IntroducedIn, last, next)
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
		"a pseudo-version leading to gates_from still warns": {
			pin: "v1.0.1-0.20261030000000-aaaaaaaaaaaa", wantSev: lintSevWarning, wantGrace: true,
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
	ro, ok := lintRollouts[rolloutRule]
	if !ok || ro.GatesFrom == "" {
		t.Fatalf("%s must carry a rollout row for this test", rolloutRule)
	}
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
