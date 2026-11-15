// File: internal/cli/lint/lint_skip_test.go
//
// A gating lane that could NOT run fails `forge lint` by default; --allow-skip
// / lint.allow_skip accept one by name with a reason; --strict does not enter
// into it; and a lane --scope does not select is not a lane that could not
// run. See lint_skip.go.

package lint

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/projectstore"
)

// collectGraded is step.collect as the structured drivers see it: the lane's
// findings after applyGapPolicy grades a lane that could not run.
func collectGraded(t *testing.T, step linterStep, rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	t.Helper()
	fs, gated, err := step.collect(rc)
	if err != nil {
		return fs, gated, err
	}
	o := lintStepOutcome{name: step.name, gates: step.gates, ran: true, findings: fs, gated: gated}
	applyGapPolicy(&o, laneGates(step, rc), rc)
	return o.findings, o.gated, nil
}

// captureStdout is captureStderr for stdout, where the text driver prints
// its verdict.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	return func() string {
		_ = w.Close()
		os.Stdout = orig
		return <-done
	}
}

// bufMissingProject is a project with a buf.yaml on a machine with no buf:
// the canonical "a tool is missing" lane.
func bufMissingProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "buf.yaml"), []byte("version: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inDir(t, dir)
	noForgeYAML()
	t.Setenv("PATH", t.TempDir())
	// No pinned buf in forge's tool cache either (the lane falls back to
	// it), and with no go on PATH none can be built.
	t.Setenv("FORGE_TOOLS_CACHE", t.TempDir())
	return dir
}

// noForgeYAML makes the registered project-store loader report "not a forge
// project", as the real one does for a fixture with no forge.yaml, so a test
// does not depend on another having installed one.
func noForgeYAML() {
	factory.SetProjectStoreLoader(func() (*projectstore.Store, error) {
		return nil, ErrProjectConfigNotFound
	})
}

// runBufLaneText renders the buf lint lane alone the way `forge lint`
// prints it (the outcome engine, not --quiet) and returns its report and
// verdict.
func runBufLaneText(t *testing.T, opts lintRunOptions) (string, error) {
	t.Helper()
	rc := &lintRunCtx{ctx: context.Background(), strict: opts.strict, allowSkip: opts.allowSkip, cfg: opts.cfg}
	var out strings.Builder
	steps := []linterStep{findStep(t, "buf lint"), cleanGatingStep("contract linter")}
	err := renderLintOutcomes(&out, collectLintOutcomesFrom(rc, steps), false, nil)
	return out.String(), err
}

// ── a missing tool fails the run, naming the lane ───────────────────────────

func TestMissingToolFailsTheRunNamingTheLane(t *testing.T) {
	bufMissingProject(t)

	t.Run("text", func(t *testing.T) {
		_, err := runBufLaneText(t, lintRunOptions{})
		if err == nil {
			t.Fatal("forge lint passed with a gating lane (buf lint) that could NOT run")
		}
		for _, want := range []string{"could NOT run", "buf lint", "buf is not on PATH", "forge tools install buf", "--allow-skip 'buf-lint=<why>'"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("verdict does not say %q:\n%v", want, err)
			}
		}
	})

	t.Run("structured", func(t *testing.T) {
		rc := &lintRunCtx{ctx: context.Background()}
		outcomes := collectLintOutcomesFrom(rc, []linterStep{findStep(t, "buf lint"), cleanGatingStep("contract linter")})
		var out strings.Builder
		err := renderLintOutcomes(&out, outcomes, true, nil)
		if err == nil || !strings.Contains(err.Error(), "could NOT run: buf lint") {
			t.Fatalf("--quiet verdict = %v; want a failure naming buf lint\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "forge tools install buf") {
			t.Errorf("--quiet does not print the fix:\n%s", out.String())
		}
		fs, gated := flattenLintOutcomes(outcomes)
		if report := buildLintJSONReport(fs, gated); report.OK {
			t.Error(`--json says "ok": true over a gating lane that could not run`)
		}
		if f := findRule(t, fs, ruleBufLintUnavailable); f.Severity != lintSevError {
			t.Errorf("gap severity = %q, want error", f.Severity)
		}
	})
}

// ── a missing node_modules fails the run, naming the lane ───────────────────

// dashboardWithoutDeps is a declared frontend with a lint script and no
// node_modules anywhere: the frontend lint lane could not run.
func dashboardWithoutDeps(t *testing.T) *config.ProjectConfig {
	t.Helper()
	root := t.TempDir()
	// Under frontends/: the lane's shouldRun finds frontends by
	// directory when the cwd has no forge.yaml.
	feDir := filepath.Join(root, "frontends", "dashboard")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feDir, "package.json"), []byte(`{"scripts":{"lint":"eslint ."}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	inDir(t, root)
	noForgeYAML()
	return &config.ProjectConfig{Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "dashboard"}.WithDir("frontends/dashboard")}}
}

func TestMissingNodeModulesFailsTheRunNamingTheLane(t *testing.T) {
	cfg := dashboardWithoutDeps(t)
	rc := &lintRunCtx{ctx: context.Background(), cfg: cfg}
	steps := []linterStep{findStep(t, "frontend lint"), cleanGatingStep("contract linter")}

	var out strings.Builder
	err := renderLintOutcomes(&out, collectLintOutcomesFrom(rc, steps), true, nil)
	if err == nil {
		t.Fatalf("forge lint passed over a frontend whose eslint never ran:\n%s", out.String())
	}
	for _, want := range []string{"frontend lint", "node_modules not found", "npm install` in frontends/dashboard", "frontend-lint=<why>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("verdict does not say %q:\n%v", want, err)
		}
	}
}

// ── --allow-skip accepts it, and the verdict lists it ───────────────────────

func TestAllowSkipPassesAndListsTheLaneAsSkippedAllowed(t *testing.T) {
	t.Run("text, missing tool", func(t *testing.T) {
		bufMissingProject(t)
		allow, err := parseSkipAllowances([]string{"buf-lint=proto lint runs in its own job"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := runBufLaneText(t, lintRunOptions{allowSkip: allow})
		if err != nil {
			t.Fatalf("an accepted skip still failed the run: %v\n%s", err, out)
		}
		if !strings.Contains(out, "1 SKIPPED (allowed): buf lint — proto lint runs in its own job") {
			t.Errorf("verdict does not list the accepted lane with its reason:\n%s", out)
		}
		if strings.Contains(out, "All ") {
			t.Errorf("verdict rounds an accepted skip up to a full pass:\n%s", out)
		}
	})

	t.Run("structured, missing node_modules, forge.yaml", func(t *testing.T) {
		cfg := dashboardWithoutDeps(t)
		cfg.Lint.AllowSkip = []config.LintSkipAllowance{{Lane: "frontend lint", Reason: "the web job lints frontends"}}
		allow, err := parseSkipAllowances(nil, cfg)
		if err != nil {
			t.Fatal(err)
		}
		rc := &lintRunCtx{ctx: context.Background(), cfg: cfg, allowSkip: allow}
		outcomes := collectLintOutcomesFrom(rc, []linterStep{findStep(t, "frontend lint"), cleanGatingStep("contract linter")})
		var out strings.Builder
		if err := renderLintOutcomes(&out, outcomes, true, nil); err != nil {
			t.Fatalf("an accepted skip still failed the run: %v", err)
		}
		if !strings.Contains(out.String(), "SKIPPED (allowed): frontend lint — the web job lints frontends") {
			t.Errorf("verdict does not list the accepted lane:\n%s", out.String())
		}
		fs, gated := flattenLintOutcomes(outcomes)
		if !buildLintJSONReport(fs, gated).OK {
			t.Error("--json is not ok over an accepted skip")
		}
		f := findRule(t, fs, ruleFrontendLintUnavailable)
		if f.Severity != lintSevInfo || !strings.Contains(f.Message, "SKIPPED (allowed: the web job lints frontends)") {
			t.Errorf("accepted gap finding = %+v; want info, naming the reason", f)
		}
		if tally := tallyFromOutcomes(outcomes); len(tally.allowed) != 1 || tally.failed {
			t.Errorf("gate tally = %+v; want one allowed lane and no failure", tally)
		}
	})
}

// TestAllowSkipDoesNotExcuseALaneThatRanAndFailed: the acceptance is for
// "could not run" only.
func TestAllowSkipDoesNotExcuseALaneThatRanAndFailed(t *testing.T) {
	failing := cleanGatingStep("buf lint")
	failing.collect = func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
		return []lintJSONFinding{{Severity: lintSevError, Rule: "buf", Message: "a.proto:1: bad"}}, true, nil
	}
	rc := &lintRunCtx{ctx: context.Background(), allowSkip: skipAllowances{"buf lint": "why not"}}
	var out strings.Builder
	if err := renderLintOutcomes(&out, collectLintOutcomesFrom(rc, []linterStep{failing}), true, nil); err == nil {
		t.Fatal("--allow-skip excused a lane that ran and reported an error")
	}
}

// ── --strict alone does not change skip handling ────────────────────────────

func TestStrictDoesNotChangeSkipHandling(t *testing.T) {
	bufMissingProject(t)

	_, plain := runBufLaneText(t, lintRunOptions{})
	_, strict := runBufLaneText(t, lintRunOptions{strict: true})
	if plain == nil || strict == nil {
		t.Fatalf("a gating lane that could not run must fail with and without --strict: plain=%v strict=%v", plain, strict)
	}
	if plain.Error() != strict.Error() {
		t.Errorf("--strict changed the verdict on a lane that could not run:\n plain:  %v\n strict: %v", plain, strict)
	}

	allow := skipAllowances{"buf lint": "no buf on this runner"}
	if out, err := runBufLaneText(t, lintRunOptions{strict: true, allowSkip: allow}); err != nil {
		t.Errorf("--strict overrode an accepted skip: %v\n%s", err, out)
	}
}

// TestAdvisoryLaneGapIsAnAdvisoryFinding pins the other half of --strict's
// one meaning: an advisory lane's could-not-run report is an advisory
// finding, so it warns by default and --strict makes it an error.
func TestAdvisoryLaneGapIsAnAdvisoryFinding(t *testing.T) {
	advisory := gapStep("typed-config guardrail", false, "")
	for _, tc := range []struct {
		strict bool
		want   gapVerdict
	}{{false, gapWarns}, {true, gapFails}} {
		o := collectLintOutcomesFrom(&lintRunCtx{ctx: context.Background(), strict: tc.strict}, []linterStep{advisory})[0]
		if o.gap == nil || o.gap.verdict != tc.want || o.gated != (tc.want == gapFails) {
			t.Errorf("strict=%v: gap=%+v gated=%v; want verdict %v", tc.strict, o.gap, o.gated, tc.want)
		}
	}
}

// TestTypecheckDialledToWarnIsAdvisoryWhenItCannotRun: a lane whose findings
// the project made advisory cannot fail the run by being absent.
func TestTypecheckDialledToWarnIsAdvisoryWhenItCannotRun(t *testing.T) {
	step := findStep(t, "frontend typecheck")
	cfg := &config.ProjectConfig{}
	if !laneGates(step, &lintRunCtx{cfg: cfg}) {
		t.Fatal("the typecheck lane must gate by default")
	}
	cfg.Lint.Frontend.Typecheck = "warn"
	if laneGates(step, &lintRunCtx{cfg: cfg}) {
		t.Error("lint.frontend.typecheck: warn must make the lane advisory for a could-not-run too")
	}
}

// ── --scope exclusions are "not selected", not "could not run" ──────────────

func TestScopeExclusionsDoNotFail(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"internal/x", "frontends/web"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scope, err := parseLintScope(root, []string{"internal/x"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("whole-project lane", func(t *testing.T) {
		wholeProject := gapStep("frontend lint", true, "")
		wholeProject.scope = scopeWholeProject
		rc := &lintRunCtx{ctx: context.Background(), scope: scope}
		var out strings.Builder
		err := renderLintOutcomes(&out, collectLintOutcomesFrom(rc, []linterStep{wholeProject, cleanGatingStep("go-markers lint")}), true, scope)
		if err != nil {
			t.Fatalf("a whole-project lane --scope did not select failed the run: %v", err)
		}
		if got := out.String(); !strings.Contains(got, "not scoped, so not run: frontend lint") || strings.Contains(got, "could NOT run") {
			t.Errorf("an unselected lane must be reported as not scoped, not as could-not-run:\n%s", got)
		}
	})

	t.Run("a gap outside the scope", func(t *testing.T) {
		// The typecheck lane is scoped by file: a frontend outside the
		// slice that could not be checked is not this slice's gap.
		outside := gapStep("frontend typecheck", true, "frontends/web")
		rc := &lintRunCtx{ctx: context.Background(), scope: scope}
		var out strings.Builder
		if err := renderLintOutcomes(&out, collectLintOutcomesFrom(rc, []linterStep{outside}), true, scope); err != nil {
			t.Fatalf("a gap outside --scope failed the scoped run: %v", err)
		}
		// Control: unscoped, the same lane fails.
		out.Reset()
		if err := renderLintOutcomes(&out, collectLintOutcomesFrom(&lintRunCtx{ctx: context.Background()}, []linterStep{outside}), true, nil); err == nil {
			t.Fatal("control: unscoped, a gating lane that could not run must fail")
		}
	})
}

// ── parsing the acceptance ──────────────────────────────────────────────────

func TestParseSkipAllowances(t *testing.T) {
	cfg := &config.ProjectConfig{}
	cfg.Lint.AllowSkip = []config.LintSkipAllowance{{Lane: "buf-lint", Reason: "from forge.yaml"}}
	got, err := parseSkipAllowances([]string{
		"frontend-lint,Frontend Typecheck=the web job covers both",
		"buf lint=from the flag",
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := skipAllowances{
		"frontend lint":      "the web job covers both",
		"frontend typecheck": "the web job covers both",
		"buf lint":           "from the flag", // the flag beats the file
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}

	for name, tc := range map[string]struct {
		flags []string
		cfg   []config.LintSkipAllowance
		want  string
	}{
		"no reason":          {flags: []string{"buf-lint"}, want: "has no reason"},
		"empty reason":       {flags: []string{"buf-lint=  "}, want: "needs a reason"},
		"unknown lane":       {flags: []string{"buff-lint=x"}, want: "is not a forge lint lane"},
		"yaml without why":   {cfg: []config.LintSkipAllowance{{Lane: "buf lint"}}, want: "lint.allow_skip"},
		"yaml unknown lane":  {cfg: []config.LintSkipAllowance{{Lane: "nope", Reason: "x"}}, want: "golangci-lint"},
		"lane list ends bad": {flags: []string{"buf-lint,=x"}, want: "is not a forge lint lane"},
	} {
		t.Run(name, func(t *testing.T) {
			c := &config.ProjectConfig{}
			c.Lint.AllowSkip = tc.cfg
			_, err := parseSkipAllowances(tc.flags, c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v; want one containing %q", err, tc.want)
			}
		})
	}
}

// ── fixtures ────────────────────────────────────────────────────────────────

// cleanGatingStep is a gating lane that runs and finds nothing, so a verdict
// has something that ran.
func cleanGatingStep(name string) linterStep {
	return linterStep{
		name: name, gates: true, scope: scopeByFile,
		shouldRun: func(*lintRunCtx) (bool, string) { return true, "" },
		collect:   func(*lintRunCtx) ([]lintJSONFinding, bool, error) { return nil, false, nil },
	}
}

// gapStep is a lane that could not run, its finding anchored at file ("" for
// none).
func gapStep(name string, gates bool, file string) linterStep {
	s := cleanGatingStep(name)
	s.gates = gates
	s.collect = func(*lintRunCtx) ([]lintJSONFinding, bool, error) {
		return []lintJSONFinding{{
			File: file, Severity: lintSevWarning, Rule: laneFlagName(name) + "-unavailable",
			Message: "the tool is missing", FixHint: "install it",
		}}, false, nil
	}
	return s
}
