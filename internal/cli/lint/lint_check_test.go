package lint

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// clearCI makes the test independent of the environment it runs in: a
// developer's shell has no CI variable, GitHub's runner has two.
func clearCI(t *testing.T) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
}

// The whole contract in one table: a gate reports, so nothing is written
// unless --fix asks, wherever it runs; --check / --no-fix beat --fix.
func TestResolveFixMode(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		flags   lintFlags
		wantFix bool
	}{
		{"local, no flags: report-only (a gate never rewrites)", nil, lintFlags{}, false},
		{"local, --fix", nil, lintFlags{fix: true}, true},
		{"local, --check", nil, lintFlags{check: true}, false},
		{"local, --no-fix", nil, lintFlags{noFix: true}, false},
		{"CI=true, no flags: report-only", map[string]string{"CI": "true"}, lintFlags{}, false},
		{"CI, --fix fixes", map[string]string{"CI": "true"}, lintFlags{fix: true}, true},
		{"--check beats --fix, as --no-fix always did", map[string]string{"CI": "true"}, lintFlags{fix: true, check: true}, false},
		{"--no-fix beats --fix locally", nil, lintFlags{fix: true, noFix: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearCI(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if fix := resolveFixMode(tc.flags); fix != tc.wantFix {
				t.Errorf("fix = %v, want %v", fix, tc.wantFix)
			}
		})
	}
}

// --check is declared, and --no-fix stays: both spell report-only.
func TestLintCheckFlagsParse(t *testing.T) {
	for args, wantCheck := range map[string]bool{"--check": true, "--no-fix": false} {
		cmd := newCmd(testFactory())
		if err := cmd.Flags().Parse([]string{args}); err != nil {
			t.Fatalf("Parse(%s): %v", args, err)
		}
		if cmd.Flags().Lookup("check") == nil || cmd.Flags().Lookup("no-fix") == nil || cmd.Flags().Lookup("fix") == nil {
			t.Fatal("--check, --no-fix and --fix must all be declared")
		}
		got, _ := cmd.Flags().GetBool("check")
		if got != wantCheck {
			t.Errorf("%s: check = %v, want %v", args, got, wantCheck)
		}
	}
}

// No flags: nothing is written, the diff each fix would apply is printed,
// and the count is non-zero so the run fails.
func TestFormatCheck_DefaultWritesNothingPrintsDiffAndFails(t *testing.T) {
	clearCI(t)
	root := newFormatProject(t)
	abs := writeFile(t, root, "internal/foo/bar.go", unformattedGo)
	other := writeFile(t, root, "internal/foo/clean.go", "package foo\n\nfunc Clean() {}\n")

	if resolveFixMode(lintFlags{}) {
		t.Fatalf("precondition: a bare `forge lint` must be report-only")
	}

	out, n := formatCheckReport(t, root, nil)
	if n != 1 {
		t.Fatalf("pending = %d, want 1 (only bar.go is unformatted)", n)
	}
	if got, _ := os.ReadFile(abs); string(got) != unformattedGo {
		t.Error("report-only mode rewrote a file")
	}
	if got, _ := os.ReadFile(other); string(got) != "package foo\n\nfunc Clean() {}\n" {
		t.Error("report-only mode touched a clean file")
	}
	report := out
	for _, want := range []string{
		"internal/foo/bar.go",
		"nothing was written",
		"--- a/internal/foo/bar.go",
		"+++ b/internal/foo/bar.go",
		"@@",
		"-        fmt.Println(os.Args)",
		"+\tfmt.Println(os.Args)",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "clean.go") {
		t.Errorf("a clean file is in the report:\n%s", report)
	}
}

// Applying the diff the report printed is exactly what the fixer does: the
// check and the fix share one walk, so they cannot disagree.
func TestFormatCheck_PendingMatchesWhatTheFixerRewrites(t *testing.T) {
	clearCI(t)
	root := newFormatProject(t)
	writeFile(t, root, "internal/a/a.go", unformattedGo)
	writeFile(t, root, "internal/b/b.go", unformattedGo)
	writeFile(t, root, "internal/c/c.go", "package c\n")

	pending, err := collectFormatPending(context.Background(), root, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := formatGoTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != len(changed) || len(pending) != 2 {
		t.Fatalf("check found %d, fixer rewrote %d, want 2 each", len(pending), len(changed))
	}
	for i, p := range pending {
		if filepath.FromSlash(p.File) != changed[i] {
			t.Errorf("check names %s, fixer rewrote %s", p.File, changed[i])
		}
	}
	// And once fixed, there is nothing left to report.
	again, _ := collectFormatPending(context.Background(), root, nil, false, nil)
	if len(again) != 0 {
		t.Errorf("a fixed tree still reports %d pending", len(again))
	}
}

// Positional paths and --scope bound the report as they bound the fix.
func TestFormatCheck_HonoursPathBounds(t *testing.T) {
	clearCI(t)
	root := newFormatProject(t)
	writeFile(t, root, "internal/mine/a.go", unformattedGo)
	writeFile(t, root, "internal/theirs/c.go", unformattedGo)

	out, n := formatCheckReport(t, root, []string{"./internal/mine/..."})
	if n != 1 {
		t.Errorf("paths: pending = %d, want 1", n)
	}
	if strings.Contains(out, "theirs") {
		t.Errorf("a package outside the named path is reported:\n%s", out)
	}

	scope := &lintScope{root: root, paths: []string{"internal/theirs"}}
	o := formatOutcome(context.Background(), root, nil, scope, nil, true)
	if !o.gated || len(o.findings) == 0 || !strings.Contains(o.findings[0].File, "theirs") {
		t.Errorf("scope outcome = %+v, want a failing format lane naming internal/theirs", o)
	}
}

// The structured drivers (--quiet, --scope, --json) get the same lane: gated
// when something is pending, with the anchored finding and the diff.
func TestFormatOutcome_GatesAndCarriesTheDiff(t *testing.T) {
	root := newFormatProject(t)
	writeFile(t, root, "internal/foo/bar.go", unformattedGo)
	o := formatOutcome(context.Background(), root, nil, nil, nil, true)
	if o.name != formatLaneName || !o.gates || !o.ran || !o.gated {
		t.Fatalf("outcome = %+v, want a gating lane that ran and failed", o)
	}
	var anchored, diff bool
	for _, f := range o.findings {
		anchored = anchored || (f.File == "internal/foo/bar.go" && f.Rule == "format" && f.Severity == lintSevError)
		diff = diff || strings.Contains(f.Message, "+++ b/internal/foo/bar.go")
	}
	if !anchored || !diff {
		t.Errorf("findings lack the anchored finding (%v) or the diff (%v): %+v", anchored, diff, o.findings)
	}

	clean := newFormatProject(t)
	writeFile(t, clean, "internal/foo/ok.go", "package foo\n")
	if o := formatOutcome(context.Background(), clean, nil, nil, nil, true); o.gated || len(o.findings) != 0 {
		t.Errorf("a formatted tree failed the format lane: %+v", o)
	}
}

// The frontend half: prettier --list-different names the files, nothing is
// written, and the diff comes from formatting on stdin.
func TestCollectFrontendFormatPending_WritesNothing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub prettier is a POSIX shell script")
	}
	root := t.TempDir()
	fe := filepath.Join(root, "frontends", "web")
	src := filepath.Join(fe, "src", "a.ts")
	writeFixture(t, src, "export const a   =  1\n")
	writeFixture(t, filepath.Join(fe, "src", "ok.ts"), "export const ok = 1;\n")
	// A stand-in prettier: --list-different names a.ts; --stdin-filepath
	// formats stdin; --write would be a bug here.
	stub := "#!/bin/sh\n" +
		"case \" $* \" in *\" --write \"*) echo WRITE >> '" + filepath.Join(root, "wrote") + "'; exit 3;; esac\n" +
		"case \" $* \" in *\" --stdin-filepath \"*) echo 'export const a = 1;'; exit 0;; esac\n" +
		"for a in \"$@\"; do case \"$a\" in --*) ;; src/a.ts) echo \"$a\";; esac; done; exit 1\n"
	bin := filepath.Join(fe, "node_modules", ".bin", "prettier")
	writeFixture(t, bin, stub)
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ProjectConfig{Frontends: []config.FrontendConfig{{Name: "web"}}}

	pending, err := collectFrontendFormatPending(context.Background(), root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].File != "frontends/web/src/a.ts" || pending[0].Tool != "prettier" {
		t.Fatalf("pending = %+v, want only frontends/web/src/a.ts", pending)
	}
	if !strings.Contains(pending[0].Diff, "+export const a = 1;") {
		t.Errorf("diff does not show the rewrite:\n%s", pending[0].Diff)
	}
	if got, _ := os.ReadFile(src); string(got) != "export const a   =  1\n" {
		t.Error("report-only mode rewrote a frontend file")
	}
	if _, err := os.Stat(filepath.Join(root, "wrote")); err == nil {
		t.Error("prettier was run with --write in report-only mode")
	}
}

// formatWithFrontends: the frontend formatter cannot be confined to Go paths
// or a scope, so it runs only for the whole project.
func TestFormatWithFrontends(t *testing.T) {
	if !formatWithFrontends(nil, nil, false) {
		t.Error("whole project, frontends on: want the frontend check")
	}
	if formatWithFrontends(nil, nil, true) {
		t.Error("--skip-frontends must skip it")
	}
	if formatWithFrontends(nil, []string{"./internal/x/..."}, false) {
		t.Error("named Go paths must skip it")
	}
	if formatWithFrontends(&lintScope{paths: []string{"x"}}, nil, false) {
		t.Error("--scope must skip it")
	}
}

// formatCheckReport renders the report-only format lane as `forge lint`
// prints it and returns the report and how many files it would rewrite.
func formatCheckReport(t *testing.T, root string, paths []string) (string, int) {
	t.Helper()
	o := formatOutcome(context.Background(), root, nil, nil, paths, true)
	var out strings.Builder
	_ = renderLintOutcomes(&out, []lintStepOutcome{o}, false, nil)
	n := 0
	for _, f := range o.findings {
		if f.Rule == "format" {
			n++
		}
	}
	return out.String(), n
}
