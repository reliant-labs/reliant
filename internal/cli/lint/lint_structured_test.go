// File: internal/cli/lint/lint_structured_test.go
//
// `forge lint --scope` filtering and `forge lint --quiet` formatting.

package lint

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// scopeFixture lays out a small tree: a Go package, a proto dir with no Go,
// and a sibling directory whose name shares a prefix with the package.
func scopeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"internal/handlers/jobs/service.go":        "package jobs\n",
		"internal/handlers/jobs2/service.go":       "package jobs2\n",
		"proto/services/jobs/v1/jobs.proto":        "syntax = \"proto3\";\n",
		"internal/handlers/jobs/README.md":         "notes\n",
		"internal/handlers/jobs/inner/x/inner.go":  "package x\n",
		"proto/services/jobs/v1/nested/extra.yaml": "a: b\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestParseLintScope(t *testing.T) {
	root := scopeFixture(t)

	s, err := parseLintScope(root, []string{"proto/services/jobs", "internal/handlers/jobs/", filepath.Join(root, "internal/handlers/jobs"), " "})
	if err != nil {
		t.Fatalf("valid scopes rejected: %v", err)
	}
	// Relative, trailing-slash and absolute spellings of one path collapse
	// to one entry; the result is sorted for a deterministic verdict line.
	if want := []string{"internal/handlers/jobs", "proto/services/jobs"}; !reflect.DeepEqual(s.paths, want) {
		t.Errorf("paths = %v, want %v", s.paths, want)
	}

	none, err := parseLintScope(root, nil)
	if err != nil || none != nil {
		t.Errorf("no --scope must mean the whole project (nil scope); got %v, %v", none, err)
	}

	for name, raw := range map[string]string{
		"missing path":  "internal/handlers/jobz",
		"outside root":  "..",
		"whole project": ".",
	} {
		if _, err := parseLintScope(root, []string{raw}); err == nil {
			t.Errorf("%s: --scope %q was accepted; a scope that matches nothing reports a clean run over no files", name, raw)
		}
	}
	if _, err := parseLintScope(root, []string{"  "}); err == nil {
		t.Error("an all-blank --scope was accepted as if it were the whole project")
	}
}

func TestLintScopeContainsAndFilter(t *testing.T) {
	root := scopeFixture(t)
	s, err := parseLintScope(root, []string{"internal/handlers/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]bool{
		"internal/handlers/jobs":                                       true,
		"internal/handlers/jobs/service.go":                            true,
		"./internal/handlers/jobs/service.go":                          true,
		filepath.Join(root, "internal/handlers/jobs/inner/x/inner.go"): true,
		// A shared name PREFIX is not containment.
		"internal/handlers/jobs2/service.go": false,
		"proto/services/jobs/v1/jobs.proto":  false,
		"internal/handlers":                  false,
	} {
		if got := s.contains(file); got != want {
			t.Errorf("contains(%q) = %v, want %v", file, got, want)
		}
	}

	kept := s.filter([]lintJSONFinding{
		{File: "internal/handlers/jobs/service.go", Rule: "in"},
		{File: "internal/handlers/jobs2/service.go", Rule: "sibling"},
		{File: "proto/services/jobs/v1/jobs.proto", Rule: "proto"},
		// No file: unattributable, so a scoped run must keep it rather
		// than hide an error it cannot place.
		{Rule: "fileless"},
	})
	var rules []string
	for _, f := range kept {
		rules = append(rules, f.Rule)
	}
	if want := []string{"in", "fileless"}; !reflect.DeepEqual(rules, want) {
		t.Errorf("filter kept %v, want %v", rules, want)
	}

	var whole *lintScope
	if !whole.contains("anything/at/all.go") || len(whole.filter([]lintJSONFinding{{File: "x"}})) != 1 {
		t.Error("a nil scope must contain everything")
	}
}

func TestLintScopeGoPackages(t *testing.T) {
	root := scopeFixture(t)
	s, err := parseLintScope(root, []string{
		"internal/handlers/jobs",             // dir with Go → recursive pattern
		"internal/handlers/jobs2/service.go", // a .go file → its package
		"proto/services/jobs",                // no Go under it → nothing
		"internal/handlers/jobs/README.md",   // a non-Go file → nothing
	})
	if err != nil {
		t.Fatal(err)
	}
	got := s.goPackages()
	sort.Strings(got)
	if want := []string{"./internal/handlers/jobs/...", "./internal/handlers/jobs2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("goPackages = %v, want %v", got, want)
	}

	protoOnly, err := parseLintScope(root, []string{"proto/services/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	// golangci-lint refuses a package pattern with no Go files under it
	// ("no go files to analyze"), so a proto-only scope yields no patterns
	// and the Go lanes skip instead of failing.
	if pkgs := protoOnly.goPackages(); len(pkgs) != 0 {
		t.Errorf("a proto-only scope produced Go package patterns %v", pkgs)
	}
}

// TestCollectLintOutcomesHonoursEachScopeMode drives the outcome engine over
// steps the test controls, one per scope mode.
func TestCollectLintOutcomesHonoursEachScopeMode(t *testing.T) {
	root := scopeFixture(t)
	scope, err := parseLintScope(root, []string{"internal/handlers/jobs"})
	if err != nil {
		t.Fatal(err)
	}

	var pkgPaths []string
	wholeProjectRan := false
	errIn := lintJSONFinding{File: "internal/handlers/jobs/service.go", Severity: lintSevError, Rule: "in-scope"}
	errOut := lintJSONFinding{File: "internal/handlers/jobs2/service.go", Severity: lintSevError, Rule: "out-of-scope"}
	always := func(*lintRunCtx) (bool, string) { return true, "" }
	steps := []linterStep{
		{
			name: "by-file-in", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return []lintJSONFinding{errIn, errOut}, true, nil },
		},
		{
			name: "by-file-out", gates: true, scope: scopeByFile, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return []lintJSONFinding{errOut}, true, nil },
		},
		{
			name: "by-packages", gates: true, scope: scopeByPackages, shouldRun: always,
			collect: func(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
				pkgPaths = rc.paths
				return nil, false, nil
			},
		},
		{
			name: "whole-project", gates: true, scope: scopeWholeProject, shouldRun: always,
			collect: func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
				wholeProjectRan = true
				return nil, true, nil
			},
		},
	}

	outcomes := collectLintOutcomesFrom(&lintRunCtx{cwd: root, paths: []string{"./..."}, scope: scope}, steps)
	byName := map[string]lintStepOutcome{}
	for _, o := range outcomes {
		byName[o.name] = o
	}

	if o := byName["by-file-in"]; !o.gated || len(o.findings) != 1 || o.findings[0].Rule != "in-scope" {
		t.Errorf("by-file lane with an in-scope error: gated=%v findings=%+v; want gated with only the in-scope finding", o.gated, o.findings)
	}
	if o := byName["by-file-out"]; o.gated || len(o.findings) != 0 {
		t.Errorf("by-file lane whose only error is OUT of scope still gates/reports: gated=%v findings=%+v", o.gated, o.findings)
	}
	if want := []string{"./internal/handlers/jobs/..."}; !reflect.DeepEqual(pkgPaths, want) {
		t.Errorf("by-packages lane ran on %v, want the scope's packages %v", pkgPaths, want)
	}
	if o := byName["whole-project"]; !o.unscoped || o.ran || wholeProjectRan {
		t.Errorf("whole-project lane under --scope: unscoped=%v ran=%v collectCalled=%v; it must be skipped and named", o.unscoped, o.ran, wholeProjectRan)
	}

	// The same steps unscoped: every lane runs on the whole project and
	// keeps its own verdict.
	outcomes = collectLintOutcomesFrom(&lintRunCtx{cwd: root, paths: []string{"./..."}}, steps)
	for _, o := range outcomes {
		if !o.ran || o.unscoped {
			t.Errorf("unscoped run did not run lane %s", o.name)
		}
	}
	if !reflect.DeepEqual(pkgPaths, []string{"./..."}) {
		t.Errorf("unscoped by-packages lane ran on %v, want ./...", pkgPaths)
	}
}

// TestEveryLintStepDeclaresAScopeMode pins that a lane cannot be added
// without deciding how --scope treats it, and that the whole-project set —
// which the --scope help text and the forge skill name — is exactly what the
// table says.
func TestEveryLintStepDeclaresAScopeMode(t *testing.T) {
	var whole []string
	for _, s := range lintPipeline() {
		switch s.scope {
		case scopeUnset:
			t.Errorf("lint step %q declares no scope mode; pick scopeByFile, scopeByPackages or scopeWholeProject", s.name)
		case scopeWholeProject:
			whole = append(whole, s.name)
		}
	}
	if want := []string{"frontend lint", "graph lint", "component-drift lint"}; !reflect.DeepEqual(whole, want) {
		t.Errorf("whole-project lanes = %v, want %v — update the --scope flag help and the forge skill's lint section with the change", whole, want)
	}
}

// structuredOutcomesFixture is one of each outcome shape the renderer
// distinguishes.
func structuredOutcomesFixture() []lintStepOutcome {
	return []lintStepOutcome{
		{name: "golangci-lint", gates: true, ran: true},
		{
			name: "read-only-fields lint", gates: true, ran: true, gated: true,
			findings: []lintJSONFinding{
				{File: "proto/services/jobs/v1/jobs.proto", Line: 58, Severity: lintSevError,
					Rule: "forgeconv-read-only-field-unwritten", Message: "Job.status is unwritten", FixHint: "write it"},
				{File: "proto/services/jobs/v1/jobs.proto", Line: 61, Severity: lintSevWarning,
					Rule: "forgeconv-read-only-field-unwritten", Message: "Job.lost_reason is unwritten (pending: implement X)"},
			},
		},
		{
			name: "scaffold ownership lint", gates: true, ran: true,
			findings: []lintJSONFinding{{File: "internal/app/providers.go", Severity: lintSevWarning,
				Rule: "scaffold-not-customized", Message: "file still contains 1 FORGE_SCAFFOLD marker(s)"}},
		},
		{name: "contract linter", gates: true, skipMsg: "contracts feature disabled — skipping contract linter"},
		{name: "frontend lint", gates: true, unscoped: true},
	}
}

func TestRenderLintOutcomesQuiet(t *testing.T) {
	var buf bytes.Buffer
	err := renderLintOutcomes(&buf, structuredOutcomesFixture(), true, nil)
	out := buf.String()

	for _, want := range []string{
		"❌ read-only-fields lint — 1 error, 1 warning",
		"✗ proto/services/jobs/v1/jobs.proto:58 [forgeconv-read-only-field-unwritten] Job.status is unwritten",
		"→ write it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quiet output missing %q:\n%s", want, out)
		}
	}
	// Only FAILING findings: no warning, no passing lane, no skip chatter.
	for _, unwanted := range []string{"lost_reason", "scaffold", "golangci-lint", "contract"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("quiet output shows %q, which did not fail:\n%s", unwanted, out)
		}
	}
	if err == nil {
		t.Fatal("a failing lane produced a passing verdict")
	}
	// The verdict — returned, so main prints it as the LAST line — names
	// what failed and admits what --quiet hid.
	for _, want := range []string{"1 gating linter(s) failed: read-only-fields lint", "2 advisory findings not shown (--quiet)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verdict %q missing %q", err, want)
		}
	}
}

// TestRenderLintOutcomesCountsIssuesNotSnippetLines pins the summary count
// for a sub-tool lane: golangci-lint prints one issue as five lines (the
// issue, the source line, a caret, a summary header and a tally), and the
// lane line must say "1 error" while still printing the snippet verbatim.
func TestRenderLintOutcomesCountsIssuesNotSnippetLines(t *testing.T) {
	outcomes := []lintStepOutcome{{
		name: "golangci-lint", gates: true, ran: true, gated: true,
		findings: externalLinesToFindings(
			"internal/handlers/jobs/service.go:85:6: func badlyFormatted is unused (unused)\n"+
				"func badlyFormatted() {}\n"+
				"     ^\n"+
				"1 issues:\n"+
				"* unused: 1\n",
			"golangci-lint", lintSevError),
	}}
	var buf bytes.Buffer
	_ = renderLintOutcomes(&buf, outcomes, true, nil)
	out := buf.String()
	if !strings.Contains(out, "❌ golangci-lint — 1 error\n") {
		t.Errorf("lane summary does not count one issue as 1 error:\n%s", out)
	}
	for _, want := range []string{
		"✗ internal/handlers/jobs/service.go:85:6 [unused] func badlyFormatted is unused (unused)",
		"   func badlyFormatted() {}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderLintOutcomesQuietCleanRunIsOneLine(t *testing.T) {
	var buf bytes.Buffer
	outcomes := []lintStepOutcome{
		{name: "golangci-lint", gates: true, ran: true},
		{name: "scaffold ownership lint", gates: true, ran: true,
			findings: []lintJSONFinding{{Severity: lintSevWarning, Rule: "scaffold-not-customized", Message: "m"}}},
	}
	if err := renderLintOutcomes(&buf, outcomes, true, nil); err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("a clean --quiet run must be ONE line (the verdict); got %d:\n%s", len(lines), buf.String())
	}
	if want := "✅ All 2 gating linters passed; 1 advisory finding not shown (--quiet)."; lines[0] != want {
		t.Errorf("verdict = %q, want %q", lines[0], want)
	}
}

func TestRenderLintOutcomesVerboseShowsWarnings(t *testing.T) {
	var buf bytes.Buffer
	_ = renderLintOutcomes(&buf, structuredOutcomesFixture(), false, nil)
	out := buf.String()
	for _, want := range []string{"lost_reason", "⚠️  scaffold ownership lint — 1 warning", "internal/app/providers.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("non-quiet structured output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderLintOutcomesScopedVerdictNamesUnscopedLanes(t *testing.T) {
	root := scopeFixture(t)
	scope, err := parseLintScope(root, []string{"internal/handlers/jobs"})
	if err != nil {
		t.Fatal(err)
	}
	outcomes := []lintStepOutcome{
		{name: "golangci-lint", gates: true, ran: true},
		{name: "frontend lint", gates: true, unscoped: true},
	}
	var buf bytes.Buffer
	if err := renderLintOutcomes(&buf, outcomes, true, scope); err != nil {
		t.Fatalf("clean scoped run failed: %v", err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	// A scoped run proves only its slice; the verdict must not read as a
	// whole-project pass, and must name the lane it left unchecked.
	for _, want := range []string{"scope internal/handlers/jobs only", "not run: frontend lint", "unscoped `forge lint` before merging"} {
		if !strings.Contains(got, want) {
			t.Errorf("scoped verdict %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "All ") {
		t.Errorf("scoped verdict claims a whole-project pass: %q", got)
	}
}

// TestLintVerdictIsTheLastLine pins the "last line" contract for the one
// verdict that prints a hint alongside it.
func TestLintVerdictIsTheLastLine(t *testing.T) {
	var buf bytes.Buffer
	if err := reportLintVerdict(&buf, []string{"golangci-lint"}, nil, []string{"frontend typecheck"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "could NOT run (frontend typecheck)") {
		t.Errorf("the verdict is not the last line; last line = %q\n%s", last, buf.String())
	}
}

func TestValidateLintModeFlags(t *testing.T) {
	for name, tc := range map[string]struct {
		flags         lintFlags
		explicitPaths bool
		wantErr       bool
	}{
		"quiet alone":           {flags: lintFlags{quiet: true}},
		"quiet + targeted":      {flags: lintFlags{quiet: true, readOnlyFields: true}},
		"quiet + scope":         {flags: lintFlags{quiet: true, scope: []string{"x"}}},
		"quiet + gate":          {flags: lintFlags{quiet: true, gateJSON: "g.json"}},
		"scope + json":          {flags: lintFlags{jsonOut: true, scope: []string{"x"}}},
		"quiet + json":          {flags: lintFlags{quiet: true, jsonOut: true}, wantErr: true},
		"scope + paths":         {flags: lintFlags{scope: []string{"x"}}, explicitPaths: true, wantErr: true},
		"scope + gate":          {flags: lintFlags{scope: []string{"x"}, gateJSON: "g.json"}, wantErr: true},
		"quiet + suggest":       {flags: lintFlags{quiet: true, suggestExcludes: true}, wantErr: true},
		"scope + suggest-buf":   {flags: lintFlags{scope: []string{"x"}, suggestBufExcepts: true}, wantErr: true},
		"plain paths, no scope": {flags: lintFlags{}, explicitPaths: true},
	} {
		if err := validateLintModeFlags(tc.flags, tc.explicitPaths); (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
		}
	}
}

// TestCollectTargetedOutcomeHonoursScope pins the targeted half: a scoped
// targeted lane is filtered like a pipeline lane, and the targeted-only
// whole-project lane is skipped and named rather than run.
func TestCollectTargetedOutcomeHonoursScope(t *testing.T) {
	root := unwrittenProject(t, estimateServiceProto, "")

	inScope, err := parseLintScope(root, []string{"proto/services/estimates"})
	if err != nil {
		t.Fatal(err)
	}
	o := collectOneTargetedOutcome(t, lintFlags{readOnlyFields: true}, root, inScope)
	if o.name != "read-only-fields" || !o.gated || len(o.findings) != 1 {
		t.Errorf("in-scope targeted lane: name=%q gated=%v findings=%+v; want the read-only error, gated", o.name, o.gated, o.findings)
	}

	outOfScope, err := parseLintScope(root, []string{"internal/handlers/estimates"})
	if err != nil {
		t.Fatal(err)
	}
	o = collectOneTargetedOutcome(t, lintFlags{readOnlyFields: true}, root, outOfScope)
	if o.gated || len(o.findings) != 0 {
		t.Errorf("a finding anchored outside the scope still reported/gated: gated=%v findings=%+v", o.gated, o.findings)
	}

	o = collectOneTargetedOutcome(t, lintFlags{configReach: true}, root, outOfScope)
	if !o.unscoped || o.ran {
		t.Errorf("--config-reach under --scope: unscoped=%v ran=%v; it is whole-project and must be skipped and named", o.unscoped, o.ran)
	}
}

// collectOneTargetedOutcome runs flags, which name exactly one lane, through
// collectTargetedOutcomes and returns that lane's outcome.
func collectOneTargetedOutcome(t *testing.T, flags lintFlags, root string, scope *lintScope) lintStepOutcome {
	t.Helper()
	outcomes, targeted, err := collectTargetedOutcomes(t.Context(), flags, nil, root, nil, nil, scope, nil)
	if err != nil || !targeted || len(outcomes) != 1 {
		t.Fatalf("one lane named: targeted=%v outcomes=%d err=%v", targeted, len(outcomes), err)
	}
	return outcomes[0]
}

// subtotalOnlyRPC populates the fixture's forge:computed subtotal_cents and
// leaves its forge:read-only total_cents unwritten: --computed-fields passes,
// --read-only-fields fails. computed-fields comes first in dispatch order,
// which is exactly the shape G6 hid — the first named lane passes and the
// verdict never sees the second.
const subtotalOnlyRPC = `package estimates

func (s *Service) RecalculateEstimate(row *Estimate) {
	row.SubtotalCents = 100
}
`

// Every lane a combined invocation names runs, and the one verdict covers
// all of them. `forge lint --conventions --contract` used to run only the
// lane first in dispatch order and print ✅ over the one it skipped.
func TestCollectTargetedOutcomes_EveryNamedLaneRuns(t *testing.T) {
	root := unwrittenProject(t, estimateServiceProto, subtotalOnlyRPC)

	alone := collectOneTargetedOutcome(t, lintFlags{computedFields: true}, root, nil)
	if !alone.ran || alone.gated {
		t.Fatalf("fixture: --computed-fields alone must pass; ran=%v gated=%v findings=%+v", alone.ran, alone.gated, alone.findings)
	}

	outcomes, targeted, err := collectTargetedOutcomes(t.Context(),
		lintFlags{readOnlyFields: true, computedFields: true}, nil, root, nil, nil, nil, nil)
	if err != nil || !targeted {
		t.Fatalf("targeted=%v err=%v", targeted, err)
	}
	var names []string
	for _, o := range outcomes {
		if !o.ran {
			t.Errorf("lane %s was named and did not run", o.name)
		}
		names = append(names, o.name)
	}
	if want := []string{"computed-fields", "read-only-fields"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("lanes = %v, want %v", names, want)
	}
	if outcomes[0].gated || !outcomes[1].gated {
		t.Errorf("computed-fields gated=%v (want false), read-only-fields gated=%v (want true)", outcomes[0].gated, outcomes[1].gated)
	}

	// --quiet / --scope: the verdict names the failing lane and counts both.
	var out bytes.Buffer
	verdict := renderLintOutcomes(&out, outcomes, true, nil)
	if verdict == nil || !strings.Contains(verdict.Error(), "1 gating linter(s) failed: read-only-fields") {
		t.Errorf("verdict = %v; want the read-only-fields failure\n%s", verdict, out.String())
	}
	if strings.Contains(out.String(), "✅") {
		t.Errorf("a failing combined run printed a success line:\n%s", out.String())
	}

	// --json: one report over both lanes.
	findings, gated := flattenLintOutcomes(outcomes)
	report := buildLintJSONReport(findings, gated)
	if report.OK {
		t.Error("--json reported ok=true over a failing named lane")
	}
	rules := map[string]bool{}
	for _, f := range report.Findings {
		rules[f.Rule] = true
	}
	if !rules[readOnlyFieldRuleID] {
		t.Errorf("--json lost the second lane's findings: %+v", report.Findings)
	}

	// --gate-json: the tally records both lanes as run.
	if tally := tallyFromOutcomes(outcomes); !reflect.DeepEqual(tally.ran, []string{"computed-fields", "read-only-fields"}) || !tally.failed {
		t.Errorf("tally = %+v; want both lanes ran, failed", tally)
	}
}

// The text driver: a combined run executes each lane and returns one verdict
// naming the lane that failed, instead of the first lane's ✅.
func TestRunLint_CombinedLanesTextVerdict(t *testing.T) {
	t.Chdir(unwrittenProject(t, estimateServiceProto, subtotalOnlyRPC))
	// The fixture has no forge.yaml: the lanes run on their defaults.
	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		return nil, ErrProjectConfigNotFound
	})

	if err := runLintStructured(t.Context(), lintFlags{computedFields: true}, nil); err != nil {
		t.Fatalf("fixture: --computed-fields alone must pass: %v", err)
	}
	err := runLintStructured(t.Context(), lintFlags{computedFields: true, readOnlyFields: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "1 gating linter(s) failed: read-only-fields") {
		t.Fatalf("combined text run = %v; want the verdict to name read-only-fields", err)
	}
	if strings.Contains(err.Error(), "computed-fields") {
		t.Errorf("the verdict names a lane that passed: %v", err)
	}
}
