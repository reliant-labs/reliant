package lint

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func lanesCfg(lanes map[string]string) *config.ProjectConfig {
	cfg := &config.ProjectConfig{}
	cfg.Lint.Lanes = lanes
	return cfg
}

func TestLanesOff_ResolvesEverySpelling(t *testing.T) {
	off, err := lanesOff(lanesCfg(map[string]string{
		"frontend":               "off", // alias: all three frontend lanes
		"migration-safety":       "off", // flag spelling without -lint
		"db grants lint":         "off", // verdict spelling
		"secrets-lint":           "on",  // listed, still running
		"format":                 "off",
		"typed-config-guardrail": "false",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"frontend lint", "frontend typecheck", "frontend lockfile",
		"migration safety lint", "db-grants lint", formatLaneName, "typed-config guardrail"} {
		if !off[lane] {
			t.Errorf("%q is not off", lane)
		}
	}
	if off["secrets lint"] {
		t.Error("secrets-lint: on switched the lane off")
	}
}

func TestLanesOff_UnknownLaneIsRefused(t *testing.T) {
	_, err := lanesOff(lanesCfg(map[string]string{"frontnd": "off"}))
	if err == nil || !strings.Contains(err.Error(), `"frontnd" is not a forge lint lane`) || !strings.Contains(err.Error(), "migration-safety-lint") {
		t.Fatalf("err = %v, want a refusal naming the lane and the valid set", err)
	}
}

// An off lane never runs, and it is never silently missing: the report gives
// it a line, the verdict counts it, --json carries it, and the gate tally
// lists it.
func TestLanesOff_OffLaneIsNamedNeverSilent(t *testing.T) {
	ran := false
	frontend := cleanGatingStep("frontend lint")
	frontend.collect = func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
		ran = true
		return []lintJSONFinding{{Severity: lintSevError, Rule: "x", Message: "would fail"}}, true, nil
	}
	rc := &lintRunCtx{lanesOff: map[string]bool{"frontend lint": true}}
	outcomes := collectLintOutcomesFrom(rc, []linterStep{frontend, cleanGatingStep("contract linter")})
	if ran {
		t.Fatal("an off lane ran")
	}

	var out strings.Builder
	if err := renderLintOutcomes(&out, outcomes, false, nil); err != nil {
		t.Fatalf("an off lane failed the run: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "○ frontend lint — off (forge.yaml lint.lanes)") {
		t.Errorf("report has no line for the off lane:\n%s", text)
	}
	last := strings.TrimSpace(text[strings.LastIndex(strings.TrimSpace(text), "\n")+1:])
	if !strings.Contains(last, "1 off (forge.yaml lint.lanes): frontend lint") || strings.Contains(last, "All ") {
		t.Errorf("verdict does not count the off lane: %q", last)
	}

	var quiet strings.Builder
	if err := renderLintOutcomes(&quiet, outcomes, true, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(quiet.String(), "off (forge.yaml lint.lanes): frontend lint") {
		t.Errorf("--quiet verdict drops the off lane:\n%s", quiet.String())
	}

	findings, _ := flattenLintOutcomes(outcomes)
	found := false
	for _, f := range findings {
		found = found || strings.Contains(f.Message, "frontend lint: off (forge.yaml lint.lanes)")
	}
	if !found {
		t.Error("--json carries no finding for the off lane")
	}
	if tally := tallyFromOutcomes(outcomes); !strings.Contains(strings.Join(tally.skipped, ","), "frontend lint (off") {
		t.Errorf("gate tally skipped = %v", tally.skipped)
	}
}

// `frontend-lint: off` switches the eslint/stylelint lane off and nothing
// else: the gating `frontend typecheck` lane still runs, in text mode and in
// --json alike (both drivers take their skips from frontendSkips), and only
// the format lane also leaves that frontend alone. --skip-frontends still
// drops the whole frontend lane in both.
func TestLanesOff_FrontendLintOffKeepsTypecheckInBothDrivers(t *testing.T) {
	off, err := lanesOff(lanesCfg(map[string]string{"frontend-lint": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if !off["frontend lint"] || off["frontend typecheck"] {
		t.Fatalf("lanes off = %v, want frontend lint alone", off)
	}
	run, format := frontendSkips(false, off)
	if run || !format {
		t.Fatalf("frontendSkips = run %v, format %v; want typecheck kept (run=false) and format skipped", run, format)
	}
	opts := jsonRunOptions(lintFlags{}, skipAllowances{}, off, nil, nil, nil)
	if opts.skipFrontends || !opts.formatSkipFrontends {
		t.Fatalf("--json run: skipFrontends %v, formatSkipFrontends %v; want typecheck kept, format skipped", opts.skipFrontends, opts.formatSkipFrontends)
	}
	if !off[formatLaneName] && opts.lanesOff["frontend typecheck"] {
		t.Fatal("--json run turned the typecheck lane off")
	}

	run, format = frontendSkips(true, nil)
	if !run || !format {
		t.Fatalf("--skip-frontends: run %v, format %v; want both skipped", run, format)
	}
	if opts := jsonRunOptions(lintFlags{skipFrontends: true}, skipAllowances{}, nil, nil, nil, nil); !opts.skipFrontends {
		t.Fatal("--json with --skip-frontends kept the frontend lane")
	}
}
