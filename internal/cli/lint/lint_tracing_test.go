package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/linter/tracinglint"
)

const tracingFixture = `package svc

import (
	"context"
	"log/slog"
)

func A(ctx context.Context) {
	slog.Info("a")
}

func B(ctx context.Context) {
	// forge:lint-disable-next-line forge-tracing-log-without-ctx: fixture keeps one call as written
	slog.Info("b")
}
`

func writeTracingProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range map[string]string{
		"go.mod":              "module example.com/app\n\ngo 1.24\n",
		"internal/svc/svc.go": tracingFixture,
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func readSvc(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "internal/svc/svc.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The fix policy: the full sweep rewrites only at error; naming the lane
// rewrites at warn; off and a suppression directive are never rewritten.
func TestTracingFixPolicy(t *testing.T) {
	warn := &config.ProjectConfig{Lint: config.LintConfig{Rules: map[string]string{tracinglint.RuleLogWithoutCtx: "warn"}}}
	off := &config.ProjectConfig{Lint: config.LintConfig{Rules: map[string]string{tracinglint.RuleLogWithoutCtx: "off"}}}

	cases := []struct {
		name      string
		cfg       *config.ProjectConfig
		explicit  bool
		wantFixed bool
		wantFinds int
	}{
		{"sweep at error fixes", nil, false, true, 0},
		{"sweep at warn reports", warn, false, false, 1},
		{"lane at warn fixes", warn, true, true, 0},
		{"off neither fixes nor reports", off, true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTracingProject(t)
			run, err := tracingFindings(dir, tc.cfg, true, tc.explicit, nil)
			if err != nil {
				t.Fatal(err)
			}
			src := readSvc(t, dir)
			fixed := strings.Contains(src, `slog.InfoContext(ctx, "a")`)
			if fixed != tc.wantFixed {
				t.Errorf("fixed = %v, want %v:\n%s", fixed, tc.wantFixed, src)
			}
			if !strings.Contains(src, `slog.Info("b")`) {
				t.Errorf("a suppressed call was rewritten:\n%s", src)
			}
			// lint.rules grades what is reported in the outcome engine.
			if graded, _ := gradeLane(effectiveRules(tc.cfg), findingsToJSON(run.findings), false); len(graded) != tc.wantFinds {
				t.Errorf("findings = %+v, want %d", graded, tc.wantFinds)
			}
		})
	}
}

// A project with observability off runs no traced process of its own, so its
// internal/ packages are out of scope (forge itself: a CLI whose pkg/ runs in
// its importers' traced servers).
func TestTracingObservabilityOffLintsLibraryCodeOnly(t *testing.T) {
	dir := writeTracingProject(t)
	off := false
	cfg := &config.ProjectConfig{Features: config.FeaturesConfig{Observability: &off}}
	run, err := tracingFindings(dir, cfg, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.findings) != 0 {
		t.Fatalf("internal/ linted with observability off: %+v", run.findings)
	}
	if run, _ := tracingFindings(dir, nil, false, false, nil); len(run.findings) != 1 {
		t.Fatalf("observability on: findings = %+v, want the internal/ one", run.findings)
	}
}

func TestTracingRejectsMalformedWrapper(t *testing.T) {
	dir := writeTracingProject(t)
	cfg := &config.ProjectConfig{Lint: config.LintConfig{Tracing: config.TracingLintConfig{
		LogWrappers: []config.LogWrapperConfig{{Call: "github.com/acme/app/internal/logging"}},
	}}}
	_, err := tracingFindings(dir, cfg, false, false, nil)
	if err == nil || !strings.Contains(err.Error(), "log_wrappers[0]") {
		t.Fatalf("err = %v, want a log_wrappers[0] error", err)
	}
}

// JSON mode is detect-only: it never rewrites, whatever the severity.
func TestTracingJSONNeverFixes(t *testing.T) {
	dir := writeTracingProject(t)
	fs, gated, err := collectTracingJSON(&lintRunCtx{cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !gated || len(fs) != 1 {
		t.Fatalf("findings = %+v gated = %v, want one gating finding", fs, gated)
	}
	if src := readSvc(t, dir); !strings.Contains(src, `slog.Info("a")`) {
		t.Fatalf("--json rewrote the file:\n%s", src)
	}
}

// writeTracingProjectTwoPackages is writeTracingProject plus a second
// package, internal/other, with its own fixable log call.
func writeTracingProjectTwoPackages(t *testing.T) string {
	t.Helper()
	dir := writeTracingProject(t)
	p := filepath.Join(dir, "internal", "other", "other.go")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	src := strings.Replace(tracingFixture, "package svc", "package other", 1)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// --fix rewrites only files inside the run's --scope, or inside the package
// paths it was given. The rewrite once ran project-wide while only the REPORT
// was scoped: a `forge lint --fix --scope pkg/sdk` renamed `store.New` to
// `store.NewInstrumented` in cmd/…/commands.go, another agent's file in a
// shared checkout (Ranger build, friction #8). Every entry point is covered:
// the sweep's lane, and the named `--tracing --fix` lane under --scope.
func TestTracingFixStaysInsideScope(t *testing.T) {
	scopeOf := func(t *testing.T, dir string) *lintScope {
		t.Helper()
		s, err := parseLintScope(dir, []string{"internal/svc"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := []struct {
		name string
		run  func(t *testing.T, dir string)
	}{
		{"sweep under --scope", func(t *testing.T, dir string) {
			_, _, err := collectTracingJSON(&lintRunCtx{cwd: dir, fix: true, scope: scopeOf(t, dir), paths: []string{"./..."}})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"sweep with package paths", func(t *testing.T, dir string) {
			if _, _, err := collectTracingJSON(&lintRunCtx{cwd: dir, fix: true, paths: []string{"./internal/svc/..."}}); err != nil {
				t.Fatal(err)
			}
		}},
		{"named lane under --scope", func(t *testing.T, dir string) {
			report, handled, err := collectSingleLinterJSON(context.Background(), lintFlags{tracing: true, fix: true},
				[]string{"./..."}, dir, nil, nil, scopeOf(t, dir))
			if err != nil || !handled || report == nil {
				t.Fatalf("report = %v handled = %v err = %v", report, handled, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTracingProjectTwoPackages(t)
			tc.run(t, dir)
			if src := readSvc(t, dir); !strings.Contains(src, `slog.InfoContext(ctx, "a")`) {
				t.Errorf("the in-scope file was not fixed:\n%s", src)
			}
			other, err := os.ReadFile(filepath.Join(dir, "internal", "other", "other.go"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(other), `slog.Info("a")`) {
				t.Errorf("a file outside the scope was rewritten:\n%s", other)
			}
		})
	}
}

// A fix run's report names what each rewrite did. It once called every
// rewrite a log call, so a run that only renamed constructions to
// NewInstrumented reported "34 log call(s) rewritten".
func TestDescribeTracingFixNamesTheRewrite(t *testing.T) {
	cases := []struct {
		rule string
		want string
	}{
		{tracinglint.RuleLogWithoutCtx, "2 log call(s) rewritten"},
		{tracinglint.RuleContractUninstrumented, "2 contract construction(s) renamed"},
	}
	for _, c := range cases {
		got := describeTracingFix(tracinglint.Fixed{File: "a.go", Rule: c.rule, Count: 2})
		if !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: got %q, want it to start %q", c.rule, got, c.want)
		}
		if c.rule == tracinglint.RuleContractUninstrumented && strings.Contains(got, "log call") {
			t.Errorf("a construction rename is reported as a log call: %q", got)
		}
		if !tracingAutoFixable(c.rule) {
			t.Errorf("%s is rewritten by --tracing, so it must count as auto-fixable", c.rule)
		}
	}
	if tracingAutoFixable(tracinglint.RuleRawSQLOpen) {
		t.Error("raw-sql-open is not rewritten by --tracing")
	}
}
