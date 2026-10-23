package lint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// ---------------------------------------------------------------------------
// Fixture builders
//
// The lane shells out to a real compiler, so the tests install a FAKE one at
// the exact path the resolver searches (node_modules/.bin/tsc). That keeps
// them hermetic and sub-second while still exercising the real
// exec/capture/classify path — no npm install, no network. The
// toolchain-present and toolchain-absent cases are then just "write the
// script" vs "don't".
// ---------------------------------------------------------------------------

// frontendFixture describes one frontend to materialize under a temp project.
type frontendFixture struct {
	name string
	// dir is the path relative to the project root; empty means the
	// conventional frontends/<name>.
	dir string
	// scripts are the package.json scripts. A nil map writes a
	// package.json with no scripts block; skipPackageJSON omits the file.
	scripts         map[string]string
	skipPackageJSON bool
	// tsconfig writes a tsconfig.json.
	tsconfig bool
	// tscExit is the exit code the fake compiler returns; tscOutput is what
	// it prints. A negative tscExit means "install no compiler at all".
	tscExit   int
	tscOutput string
	// nodeModules materializes node_modules/ even when no compiler is
	// installed (the "deps present, compiler missing" case).
	nodeModules bool
}

// writeFrontend materializes one frontend fixture under root and returns its
// directory.
func writeFrontend(t *testing.T, root string, f frontendFixture) string {
	t.Helper()
	dir := f.dir
	if dir == "" {
		dir = filepath.Join("frontends", f.name)
	}
	abs := filepath.Join(root, dir)
	mustMkdirAll(t, filepath.Join(abs, "src"))

	if !f.skipPackageJSON {
		pkg := map[string]any{"name": f.name}
		if f.scripts != nil {
			pkg["scripts"] = f.scripts
		}
		data, err := json.Marshal(pkg)
		if err != nil {
			t.Fatalf("marshal package.json: %v", err)
		}
		mustWrite(t, filepath.Join(abs, "package.json"), string(data))
	}
	if f.tsconfig {
		mustWrite(t, filepath.Join(abs, "tsconfig.json"), `{"compilerOptions":{"strict":true}}`)
	}
	if f.tscExit >= 0 {
		writeFakeTSC(t, abs, f.tscExit, f.tscOutput)
	} else if f.nodeModules {
		mustMkdirAll(t, filepath.Join(abs, "node_modules"))
	}
	return dir
}

// writeFakeTSC installs an executable stand-in for the TypeScript compiler at
// feDir/node_modules/.bin/tsc that prints output and exits with code.
func writeFakeTSC(t *testing.T, feDir string, code int, output string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-compiler fixture is POSIX-shell based")
	}
	binDir := filepath.Join(feDir, "node_modules", ".bin")
	mustMkdirAll(t, binDir)
	script := "#!/bin/sh\n"
	if output != "" {
		script += "cat <<'FORGE_TSC_EOF'\n" + output + "\nFORGE_TSC_EOF\n"
	}
	script += "exit " + itoa(code) + "\n"
	path := filepath.Join(binDir, "tsc")
	mustWrite(t, path, script)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake tsc: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// inDir chdirs into dir for the duration of the test. The lint pipeline is
// cwd-relative by design (every step resolves paths against the project
// root), so the fixtures have to be entered, not passed.
func inDir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// cfgWithFrontends builds a project config declaring the named frontends.
func cfgWithFrontends(names ...string) *config.ProjectConfig {
	cfg := &config.ProjectConfig{Name: "demo"}
	for _, n := range names {
		cfg.Frontends = append(cfg.Frontends, config.FrontendConfig{Name: n, Type: "nextjs"})
	}
	return cfg
}

// runLane runs the typecheck lane's JSON collector against the fixture in the
// current directory, graded as the drivers grade it (applyGapPolicy), so a
// typecheck that could not run reads as it does in a real run.
func runLane(t *testing.T, cfg *config.ProjectConfig, strict bool) ([]lintJSONFinding, bool) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	fs, gated, collectErr := collectGraded(t, findStep(t, "frontend typecheck"), &lintRunCtx{
		ctx: context.Background(), cfg: cfg, cwd: cwd, strict: strict,
	})
	if collectErr != nil {
		t.Fatalf("collectFrontendTypecheckJSON: unexpected error %v", collectErr)
	}
	return fs, gated
}

func findingsWithRule(fs []lintJSONFinding, rule string) []lintJSONFinding {
	var out []lintJSONFinding
	for _, f := range fs {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestFrontendTypecheck_CleanFrontendPasses: a frontend whose compiler exits 0
// contributes no findings and does not gate. "Clean" must be silent — a lane
// that narrates its successes into the findings array makes `summary.total`
// useless as a defect count.
func TestFrontendTypecheck_CleanFrontendPasses(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{name: "web", tsconfig: true, tscExit: 0})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("web"), false)
	if gated {
		t.Error("a clean frontend must not gate the lint run")
	}
	// Only the lane's note of what it checked: no issue.
	if len(fs) != 1 || fs[0].Rule != ruleRanNote || fs[0].Severity != lintSevInfo || !strings.Contains(fs[0].Message, "web: typecheck passed") {
		t.Errorf("a clean frontend must contribute only its ran note, got %d: %+v", len(fs), fs)
	}
}

// TestFrontendTypecheck_TypeErrorFails: the whole point of the lane. A real
// type error gates and is reported with its file, line, and column so an agent
// can jump straight to it.
func TestFrontendTypecheck_TypeErrorFails(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{
		name:     "web",
		tsconfig: true,
		tscExit:  2,
		// The banner line stands in for npm's `> web@0.1.0 typecheck` echo:
		// transcript that must be preserved but must not be counted as a defect.
		tscOutput: "> web@0.1.0 typecheck\n" +
			"src/bad.ts(7,11): error TS2322: Type 'string' is not assignable to type 'number'.",
	})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("web"), false)
	if !gated {
		t.Fatal("a frontend with a type error MUST gate the lint run — this is the regression the lane exists to catch")
	}

	var diag *lintJSONFinding
	for i := range fs {
		if strings.Contains(fs[i].Message, "TS2322") {
			diag = &fs[i]
		}
	}
	if diag == nil {
		t.Fatalf("no finding carried the TS2322 diagnostic; got %+v", fs)
	}
	if diag.Severity != lintSevError {
		t.Errorf("type-error diagnostic severity = %q, want %q", diag.Severity, lintSevError)
	}
	if diag.Rule != ruleFrontendTypecheck {
		t.Errorf("type-error diagnostic rule = %q, want %q", diag.Rule, ruleFrontendTypecheck)
	}
	wantFile := filepath.Join("frontends", "web", "src/bad.ts")
	if diag.File != wantFile {
		t.Errorf("diagnostic file = %q, want %q (project-relative, so a consumer can open it from the project root)", diag.File, wantFile)
	}
	if diag.Line != 7 || diag.Col != 11 {
		t.Errorf("diagnostic position = %d:%d, want 7:11", diag.Line, diag.Col)
	}

	// The sub-tool's transcript is preserved but is NOT a defect: exactly
	// one error-severity diagnostic plus the lane's verdict. Counting npm's
	// own echo as an error makes summary.errors useless as a defect count.
	errs := 0
	sawBanner := false
	for _, f := range fs {
		if f.Severity == lintSevError {
			errs++
		}
		if strings.Contains(f.Message, "web@0.1.0 typecheck") {
			sawBanner = true
			if f.Severity != lintSevInfo {
				t.Errorf("sub-tool transcript line severity = %q, want %q", f.Severity, lintSevInfo)
			}
		}
	}
	if !sawBanner {
		t.Error("sub-tool output must be preserved, not dropped")
	}
	if errs != 2 {
		t.Errorf("error-severity findings = %d, want 2 (the lane verdict + the one real diagnostic); got %+v", errs, fs)
	}
}

// TestFrontendTypecheck_NoFrontendsIsCleanNoOp: a backend-only project must be
// a silent no-op — the step does not run at all and says nothing.
func TestFrontendTypecheck_NoFrontendsIsCleanNoOp(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "internal"))
	inDir(t, root)

	step := findStep(t, "frontend typecheck")
	cwd, _ := os.Getwd()

	for _, tc := range []struct {
		name string
		cfg  *config.ProjectConfig
	}{
		{"nil config", nil},
		{"config declaring no frontend", &config.ProjectConfig{Name: "demo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, msg := step.shouldRun(&lintRunCtx{cfg: tc.cfg, cwd: cwd})
			if run {
				t.Error("frontend typecheck must not run in a project with no frontends")
			}
			if msg != "" {
				t.Errorf("a project with no frontends must skip SILENTLY, got message %q", msg)
			}
		})
	}
}

// TestFrontendTypecheck_MissingNodeModulesFailsHonestly: the load-bearing
// honesty invariant. A check that could not run must never look like a check
// that passed — it surfaces under its own rule with a fix hint (not an
// info/skip that a consumer filters out, and not a bogus type error), and
// since the lane gates, it fails the run (lint_skip.go).
func TestFrontendTypecheck_MissingNodeModulesFailsHonestly(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{
		name:     "web",
		tsconfig: true,
		scripts:  map[string]string{"typecheck": "tsc --noEmit"},
		tscExit:  -1, // no node_modules at all
	})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("web"), false)
	if !gated {
		t.Error("a gating typecheck that could not run must fail the run — a check that did not run is not a check that passed")
	}
	un := findingsWithRule(fs, ruleFrontendTypecheckUnavailable)
	if len(un) != 1 {
		t.Fatalf("want exactly one %s finding, got %d: %+v", ruleFrontendTypecheckUnavailable, len(un), fs)
	}
	if un[0].Severity != lintSevError {
		t.Errorf("unavailable severity = %q, want %q — an info/skip finding lets a consumer mistake 'did not run' for 'passed'", un[0].Severity, lintSevError)
	}
	if !strings.Contains(un[0].Message, "node_modules") || !strings.Contains(un[0].Message, "did NOT run") {
		t.Errorf("unavailable message must name what is missing AND say the check did not run, got %q", un[0].Message)
	}
	if !strings.Contains(un[0].FixHint, "npm install") {
		t.Errorf("unavailable finding must carry the remediation, got fix_hint %q", un[0].FixHint)
	}
}

// TestFrontendTypecheck_StrictDoesNotChangeUnavailable: --strict means
// "advisory findings become errors"; a gating typecheck that could not run
// fails without it, and the same with it.
func TestFrontendTypecheck_StrictDoesNotChangeUnavailable(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{name: "web", tsconfig: true, tscExit: -1})
	inDir(t, root)

	for _, strict := range []bool{false, true} {
		fs, gated := runLane(t, cfgWithFrontends("web"), strict)
		if !gated {
			t.Fatalf("strict=%v: a gating typecheck that could not run must fail the run", strict)
		}
		un := findingsWithRule(fs, ruleFrontendTypecheckUnavailable)
		if len(un) != 1 || un[0].Severity != lintSevError {
			t.Errorf("strict=%v: want one error unavailable finding, got %+v", strict, fs)
		}
	}
}

// TestFrontendTypecheck_DepsPresentButNoCompiler: node_modules exists but
// nothing provides tsc. Reported as unavailable, NOT as a type error — the
// distinction is the difference between "install your deps" and "your code is
// broken".
func TestFrontendTypecheck_DepsPresentButNoCompiler(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{name: "web", tsconfig: true, tscExit: -1, nodeModules: true})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("web"), false)
	if !gated {
		t.Error("an unresolvable compiler is a typecheck that could not run, which fails the run")
	}
	un := findingsWithRule(fs, ruleFrontendTypecheckUnavailable)
	if len(un) != 1 {
		t.Fatalf("want one unavailable finding, got %+v", fs)
	}
	if !strings.Contains(un[0].Message, "no TypeScript compiler installed") {
		t.Errorf("message must name the missing compiler, got %q", un[0].Message)
	}
	if len(findingsWithRule(fs, ruleFrontendTypecheck)) != 0 {
		t.Error("a missing compiler must NOT be reported as a type error")
	}
}

// TestFrontendTypecheck_NoTypecheckScriptStillChecks: the gap that motivated
// splitting the lane out. A TypeScript frontend with a tsconfig but NO
// `typecheck` npm script used to get a nag and no check; now forge resolves
// the compiler and runs it.
func TestFrontendTypecheck_NoTypecheckScriptStillChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{
		name:      "web",
		tsconfig:  true,
		scripts:   map[string]string{"build": "next build"}, // no typecheck script
		tscExit:   2,
		tscOutput: "src/x.ts(1,1): error TS2304: Cannot find name 'nope'.",
	})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("web"), false)
	if !gated {
		t.Fatal("a TypeScript frontend with no `typecheck` script must STILL be typechecked — forge resolves the compiler itself")
	}
	if len(findingsWithRule(fs, ruleFrontendTypecheck)) == 0 {
		t.Errorf("want type-error findings, got %+v", fs)
	}
}

// TestFrontendTypecheck_NonTypeScriptFrontendIsSilent: a frontend with neither
// a tsconfig nor a typecheck script is not a TypeScript project. Silence, not
// a warning — nagging every plain-JS frontend would train users to ignore the
// lane.
func TestFrontendTypecheck_NonTypeScriptFrontendIsSilent(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{name: "web", scripts: map[string]string{"build": "vite build"}, tscExit: -1})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("web"), false)
	if gated || len(fs) != 0 {
		t.Errorf("a non-TypeScript frontend must contribute nothing, got gated=%v findings=%+v", gated, fs)
	}
}

// TestFrontendTypecheck_SeverityDial pins the forge.yaml severity vocabulary,
// including the `warn` spelling and its `warning` alias (a legal alias that a
// prior fix restored — it must not regress).
func TestFrontendTypecheck_SeverityDial(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	for _, tc := range []struct {
		severity  string
		wantGates bool
		wantSev   string
	}{
		{"", true, lintSevError},           // default
		{"error", true, lintSevError},      //
		{"warn", false, lintSevWarning},    //
		{"warning", false, lintSevWarning}, // legal alias for warn
		{"WARN", false, lintSevWarning},    // case-insensitive
		{"bogus", true, lintSevError},      // invalid falls back to the default
	} {
		t.Run("severity="+tc.severity, func(t *testing.T) {
			root := t.TempDir()
			writeFrontend(t, root, frontendFixture{
				name: "web", tsconfig: true, tscExit: 2,
				tscOutput: "src/bad.ts(1,1): error TS2322: nope.",
			})
			inDir(t, root)

			cfg := cfgWithFrontends("web")
			cfg.Lint.Frontend.Typecheck = tc.severity
			fs, gated := runLane(t, cfg, false)
			if gated != tc.wantGates {
				t.Errorf("gated = %v, want %v", gated, tc.wantGates)
			}
			diags := findingsWithRule(fs, ruleFrontendTypecheck)
			if len(diags) == 0 {
				t.Fatalf("want type-error findings, got %+v", fs)
			}
			if diags[0].Severity != tc.wantSev {
				t.Errorf("severity = %q, want %q", diags[0].Severity, tc.wantSev)
			}
		})
	}
}

// TestFrontendTypecheck_OffDisablesLane: `off` removes the lane, and says so
// rather than vanishing — a disabled check the user cannot see is how a
// project ends up unknowingly unchecked.
func TestFrontendTypecheck_OffDisablesLane(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{name: "web", tsconfig: true, tscExit: 2, tscOutput: "src/x.ts(1,1): error TS1: x"})
	inDir(t, root)

	cfg := cfgWithFrontends("web")
	cfg.Lint.Frontend.Typecheck = "off"

	step := findStep(t, "frontend typecheck")
	cwd, _ := os.Getwd()
	run, msg := step.shouldRun(&lintRunCtx{cfg: cfg, cwd: cwd})
	if run {
		t.Error("lint.frontend.typecheck: off must skip the lane")
	}
	if !strings.Contains(msg, "off") {
		t.Errorf("the skip must be visible and say why, got %q", msg)
	}
}

// TestFrontendTypecheck_SkipFlagAndFrameworkNone pins the two whole-lane
// suppressors, and that both announce themselves.
func TestFrontendTypecheck_SkipFlagAndFrameworkNone(t *testing.T) {
	root := t.TempDir()
	writeFrontend(t, root, frontendFixture{name: "web", tsconfig: true, tscExit: 0})
	inDir(t, root)
	cwd, _ := os.Getwd()

	frontendSteps := []string{"frontend lint", "frontend typecheck"}
	for _, name := range frontendSteps {
		step := findStep(t, name)
		run, msg := step.shouldRun(&lintRunCtx{cfg: cfgWithFrontends("web"), cwd: cwd, skipFrontends: true})
		if run {
			t.Errorf("--skip-frontends must skip %q", name)
		}
		if !strings.Contains(msg, "skip-frontends") {
			t.Errorf("%q skip message must name the flag, got %q", name, msg)
		}
	}
}

// TestFrontendTypecheckTargets_ResolvesFromProject: the set comes from
// forge.yaml (custom `path:` included), never from globbing frontends/*/ —
// with the bare-directory scan reserved for the no-config fallback.
func TestFrontendTypecheckTargets_ResolvesFromProject(t *testing.T) {
	root := t.TempDir()
	mustMkdirAll(t, filepath.Join(root, "frontends", "stale"))
	mustMkdirAll(t, filepath.Join(root, "apps", "admin"))
	inDir(t, root)

	cfg := &config.ProjectConfig{
		Name: "demo",
		Frontends: []config.FrontendConfig{
			{Name: "web"}, // conventional layout
			config.FrontendConfig{Name: "admin"}.WithDir("apps/admin"), // custom path
		},
	}
	got := frontendTypecheckTargets(cfg)
	want := []frontendTarget{
		// Slash-separated on every OS: config.FrontendConfig.Dir returns it so.
		{name: "web", dir: "frontends/web"},
		{name: "admin", dir: "apps/admin"},
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, tgt := range got {
		if tgt.name == "stale" {
			t.Error("a frontends/ subdirectory the project does not declare must not be typechecked — the set is the project's, not the glob's")
		}
	}

	// No config at all: fall back to what is obviously there.
	fallback := frontendTypecheckTargets(nil)
	if len(fallback) != 1 || fallback[0].name != "stale" {
		t.Errorf("no-config fallback must scan frontends/, got %+v", fallback)
	}
}

// TestFrontendTypecheck_MultipleFrontendsRunConcurrentlyInOrder: results must
// be reported in declaration order regardless of which compiler finishes
// first, or the report is not diffable.
func TestFrontendTypecheck_MultipleFrontendsRunConcurrentlyInOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	root := t.TempDir()
	// "slow" sleeps so it finishes LAST in wall-clock but must still be
	// reported FIRST (declaration order).
	slowDir := writeFrontend(t, root, frontendFixture{name: "slow", tsconfig: true, tscExit: 0})
	mustWrite(t, filepath.Join(root, slowDir, "node_modules", ".bin", "tsc"),
		"#!/bin/sh\nsleep 0.4\necho 'src/a.ts(1,1): error TS1: slow'\nexit 2\n")
	if err := os.Chmod(filepath.Join(root, slowDir, "node_modules", ".bin", "tsc"), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	writeFrontend(t, root, frontendFixture{
		name: "fast", tsconfig: true, tscExit: 2,
		tscOutput: "src/b.ts(1,1): error TS1: fast",
	})
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends("slow", "fast"), false)
	if !gated {
		t.Fatal("both frontends have type errors; the lane must gate")
	}
	joined := make([]string, 0, len(fs))
	for _, f := range fs {
		joined = append(joined, f.Message)
	}
	all := strings.Join(joined, "\n")
	slowAt := strings.Index(all, "slow")
	fastAt := strings.Index(all, "fast")
	if slowAt < 0 || fastAt < 0 {
		t.Fatalf("both frontends must be reported, got:\n%s", all)
	}
	if slowAt > fastAt {
		t.Error("findings must be ordered by declaration, not by completion — a report whose line order varies run to run is not diffable")
	}
}

// TestFrontendTypecheck_RunsFrontendsConcurrently: `forge lint` runs in agent
// loops where wall-clock is the cost that matters, and tsc on a real Next.js
// app is tens of seconds — so a multi-frontend project must not serialize.
//
// The fake compilers form a BARRIER: each announces itself, then waits until
// all of them have, and only then exits 0. Concurrency is therefore PROVEN by
// the run succeeding — if the lane serializes, the first compiler waits alone,
// never sees the others, and exits non-zero, which surfaces as a finding.
//
// An earlier version slept 0.4s in each compiler and asserted that every
// "start" landed before the first "end". That reads as timing-independent and
// is not: it assumes the sleep dwarfs process startup, and under a loaded
// `go test ./...` spawning four shells can exceed it, so one compiler finishes
// before the last begins. It failed roughly 1 run in 5 that way. A flaky test
// is worse than no test — it trains people to re-run red until it is green.
//
// The barrier has no such assumption. It cannot pass while serialized and
// cannot fail while concurrent, at any machine speed.
func TestFrontendTypecheck_RunsFrontendsConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns package-manager / frontend toolchain subprocesses; runs in task test")
	}
	const frontends = 4 // == frontendTypecheckConcurrency, so all may run at once

	root := t.TempDir()
	// A directory rather than a shared log: each compiler creates exactly one
	// entry, so counting is a stat, with no interleaved-append ambiguity.
	arrivals := filepath.Join(root, "arrivals")
	mustMkdirAll(t, arrivals)

	names := make([]string, 0, frontends)
	for i := range frontends {
		name := "fe" + itoa(i)
		names = append(names, name)
		dir := writeFrontend(t, root, frontendFixture{name: name, tsconfig: true, tscExit: 0})
		binPath := filepath.Join(root, dir, "node_modules", ".bin", "tsc")
		// Announce, then wait for the full cohort. The bound (~10s) only caps
		// how long a SERIALIZED lane takes to report; it is never reached when
		// the lane is concurrent, so a slow machine cannot fail this.
		mustWrite(t, binPath, "#!/bin/sh\n"+
			"touch "+filepath.Join(arrivals, name)+"\n"+
			"i=0\n"+
			"while [ \"$(ls "+arrivals+" | wc -l)\" -lt "+itoa(frontends)+" ] && [ $i -lt 200 ]; do\n"+
			"  sleep 0.05\n"+
			"  i=$((i+1))\n"+
			"done\n"+
			"[ \"$(ls "+arrivals+" | wc -l)\" -ge "+itoa(frontends)+" ] || exit 7\n"+
			"exit 0\n")
		if err := os.Chmod(binPath, 0o755); err != nil {
			t.Fatalf("chmod: %v", err)
		}
	}
	inDir(t, root)

	fs, gated := runLane(t, cfgWithFrontends(names...), false)
	if gated || len(fs) != frontends || anyIssue(fs) {
		t.Fatalf("the lane is SERIALIZING: a compiler timed out waiting for the others to start, "+
			"so it exited non-zero. gated=%v findings=%+v", gated, fs)
	}

	// Every frontend was actually checked — a lane that skipped some would
	// otherwise pass vacuously, since the survivors would still meet a smaller
	// cohort only if the barrier were smaller. Guard it explicitly.
	entries, err := os.ReadDir(arrivals)
	if err != nil {
		t.Fatalf("read arrivals: %v", err)
	}
	if len(entries) != frontends {
		t.Errorf("%d of %d compilers ran — the lane skipped a frontend", len(entries), frontends)
	}
}

// TestResolveLocalTSC_WalksToWorkspaceRootButNotAbove: npm/pnpm workspaces
// hoist a shared typescript devDep to the repo root, so the resolver climbs —
// but never above the project root, where a compiler belongs to some unrelated
// checkout.
func TestResolveLocalTSC_WalksToWorkspaceRootButNotAbove(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "project")
	feDir := filepath.Join(root, "frontends", "web")
	mustMkdirAll(t, feDir)

	if got := resolveLocalTSC(feDir, root); got != "" {
		t.Errorf("no compiler installed anywhere: resolveLocalTSC = %q, want \"\"", got)
	}

	// A compiler ABOVE the project root must be ignored.
	writeFakeTSC(t, outer, 0, "")
	if got := resolveLocalTSC(feDir, root); got != "" {
		t.Errorf("a compiler above the project root must be ignored, got %q", got)
	}

	// Hoisted to the project root: found.
	writeFakeTSC(t, root, 0, "")
	want := filepath.Join(root, "node_modules", ".bin", "tsc")
	if got := resolveLocalTSC(feDir, root); got != want {
		t.Errorf("hoisted compiler: resolveLocalTSC = %q, want %q", got, want)
	}

	// The frontend's own install wins over the hoisted one.
	writeFakeTSC(t, feDir, 0, "")
	want = filepath.Join(feDir, "node_modules", ".bin", "tsc")
	if got := resolveLocalTSC(feDir, root); got != want {
		t.Errorf("frontend-local compiler must win: got %q, want %q", got, want)
	}
}

// anyIssue reports whether fs holds anything but a lane's ran notes.
func anyIssue(fs []lintJSONFinding) bool {
	for _, f := range fs {
		if f.Rule != ruleRanNote {
			return true
		}
	}
	return false
}
