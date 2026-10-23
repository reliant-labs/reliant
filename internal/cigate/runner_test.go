package cigate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeExec scripts command outcomes by the first argv word and records what
// ran. Checks run concurrently, so it is safe for concurrent use.
type fakeExec struct {
	fail map[string]bool
	mu   sync.Mutex
	ran  []string
	env  map[string][]string
}

func (f *fakeExec) run(_ context.Context, _ string, env []string, argv []string, out io.Writer) error {
	f.mu.Lock()
	f.ran = append(f.ran, argv[0])
	if f.env == nil {
		f.env = map[string][]string{}
	}
	f.env[argv[0]] = env
	f.mu.Unlock()
	fmt.Fprintf(out, "output of %s\n", argv[0])
	if f.fail[argv[0]] {
		return errors.New("boom")
	}
	return nil
}

// ranSorted is what ran, in a stable order: concurrent checks start in any.
func (f *fakeExec) ranSorted() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.ran...)
	sort.Strings(out)
	return strings.Join(out, ",")
}

func check(name string, argv ...string) Check {
	return Check{Name: name, Lane: LaneChecks, Argv: argv}
}

func statuses(rep Report) map[string]Status {
	m := map[string]Status{}
	for _, r := range rep.Results {
		m[r.Name] = r.Status
	}
	return m
}

// Rule 1: every check runs even after one fails, the table lists them all, and
// the gate is red.
func TestRun_ReportsEveryFailureThenFails(t *testing.T) {
	fx := &fakeExec{fail: map[string]bool{"lint": true, "scan": true}}
	rep := Run(context.Background(), "checks",
		[]Check{check("lint", "lint"), check("build", "build"), check("scan", "scan")},
		Options{Out: io.Discard, Exec: fx.run})

	if got := fx.ranSorted(); got != "build,lint,scan" {
		t.Fatalf("ran %q; a failing check must not stop the others", got)
	}
	if names := strings.Join([]string{rep.Results[0].Name, rep.Results[1].Name, rep.Results[2].Name}, ","); names != "lint,build,scan" {
		t.Errorf("rows %s: the table keeps plan order whatever order checks finish in", names)
	}
	want := map[string]Status{"lint": Fail, "build": Pass, "scan": Fail}
	for name, st := range statuses(rep) {
		if st != want[name] {
			t.Errorf("%s = %s, want %s", name, st, want[name])
		}
	}
	if !rep.Failed() {
		t.Error("gate passed with two failing checks")
	}
}

// Rule 2: a check that could not run is SKIPPED, not PASS, and fails the gate.
func TestRun_SkipIsNotPassAndFailsTheGate(t *testing.T) {
	c := check("vuln-go", "govulncheck")
	c.Prereq = func() string { return "govulncheck is not on PATH" }
	fx := &fakeExec{}
	rep := Run(context.Background(), "checks", []Check{c, check("build", "build")}, Options{Out: io.Discard, Exec: fx.run})

	if st := statuses(rep)["vuln-go"]; st != Skip {
		t.Fatalf("vuln-go = %s, want SKIPPED", st)
	}
	if !rep.Failed() {
		t.Error("a silent skip passed the gate: that is false confidence")
	}
	if len(fx.ran) != 1 || fx.ran[0] != "build" {
		t.Errorf("ran %v, want only build", fx.ran)
	}
}

// CI_SKIP allows a skip. The row is still listed, as SKIPPED, with the reason.
func TestRun_CISkipAllowsButStillListsTheRow(t *testing.T) {
	fx := &fakeExec{}
	var out bytes.Buffer
	rep := Run(context.Background(), "checks",
		[]Check{check("vuln-go", "govulncheck"), check("build", "build")},
		Options{Out: io.Discard, Exec: fx.run, Skip: ParseSkips("vuln-go")})

	if rep.Failed() {
		t.Error("an explicitly allowed skip failed the gate")
	}
	if len(fx.ran) != 1 || fx.ran[0] != "build" {
		t.Errorf("ran %v; a skipped check must not run", fx.ran)
	}
	rep.RenderText(&out)
	table := out.String()
	if !strings.Contains(table, "vuln-go") || !strings.Contains(table, "SKIPPED") || !strings.Contains(table, "allowed by CI_SKIP") {
		t.Errorf("the summary must still list the allowed skip:\n%s", table)
	}
}

// A project with no integration tests has nothing to run: N/A is not a skip,
// and does not fail the gate.
func TestRun_NotApplicableIsNeitherSkipNorFailure(t *testing.T) {
	c := check("integration-tests", "task")
	c.NotApplicable = "no //go:build integration test files"
	rep := Run(context.Background(), "all", []Check{c}, Options{Out: io.Discard, Exec: (&fakeExec{}).run})
	if st := statuses(rep)["integration-tests"]; st != NA {
		t.Fatalf("= %s, want N/A", st)
	}
	if rep.Failed() {
		t.Error("N/A failed the gate")
	}
}

func TestParseAndValidateSkips(t *testing.T) {
	got := ParseSkips(" a, b\nc ,,")
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("ParseSkips = %v", got)
	}
	bad := UnknownSkips([]string{"lint", "lnit"}, []Check{check("lint", "x")})
	if len(bad) != 1 || bad[0] != "lnit" {
		t.Errorf("UnknownSkips = %v, want [lnit]: a typo must not silently skip nothing", bad)
	}
}

// Rule 4: the table renders as Markdown for $GITHUB_STEP_SUMMARY, including the
// failing check's output.
func TestRenderMarkdown(t *testing.T) {
	fx := &fakeExec{fail: map[string]bool{"lint": true}}
	rep := Run(context.Background(), "checks", []Check{check("lint", "lint"), check("build", "build")}, Options{Out: io.Discard, Exec: fx.run})
	var md bytes.Buffer
	rep.RenderMarkdown(&md)
	for _, want := range []string{"| `lint` | ❌ FAIL", "| `build` | ✅ PASS", "❌ **FAIL**", "output of lint"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown summary missing %q:\n%s", want, md.String())
		}
	}
}

// A failing tree-mutating check resets the tree it dirtied.
func TestRun_OnFailRunsAfterAFailure(t *testing.T) {
	reset := false
	c := check("format-sql", "fmt")
	c.OnFail = func(io.Writer) { reset = true }
	Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: (&fakeExec{fail: map[string]bool{"fmt": true}}).run})
	if !reset {
		t.Error("OnFail did not run after the command failed")
	}
}

// Done short-circuits (npm ci when node_modules exists) and counts as PASS.
func TestRun_DoneIsPassWithoutRunning(t *testing.T) {
	c := check("deps-web", "npm")
	c.Done = func() string { return "already installed" }
	fx := &fakeExec{}
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run})
	if statuses(rep)["deps-web"] != Pass || len(fx.ran) != 0 {
		t.Errorf("status=%v ran=%v", statuses(rep), fx.ran)
	}
}

// Rule 3: a pinned tool is resolved before the command runs, and its directory
// goes FIRST on PATH, ahead of a copy at another version.
func TestRun_PinnedToolDirIsFirstOnPATH(t *testing.T) {
	r := &Resolver{
		LookPath: func(string) (string, error) { return "/usr/bin/golangci-lint", nil },
		Probe: func(path string, _ Tool) string {
			return map[bool]string{true: "v2.14.0", false: "v2.12.2"}[strings.Contains(path, "cache")]
		},
		Toolchain: func(context.Context) string { return "go1.27.2" },
		CacheRoot: t.TempDir(),
		Install: func(_ context.Context, tool Tool, dir, _ string) error {
			return os.WriteFile(filepath.Join(dir, tool.Binary+exeSuffix()), []byte("#!/bin/sh\n"), 0o755)
		},
	}
	// The installed copy lives under CacheRoot; make Probe call it "cache".
	r.CacheRoot = filepath.Join(t.TempDir(), "cache")
	c := check("golangci-lint", "golangci-lint")
	c.Tools = []Tool{{Binary: "golangci-lint", Version: "v2.14.0"}}
	fx := &fakeExec{}
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r, Env: []string{"PATH=/usr/bin"}})
	if statuses(rep)["golangci-lint"] != Pass {
		t.Fatalf("rows: %+v", rep.Results)
	}
	var path string
	for _, kv := range fx.env["golangci-lint"] {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	first := strings.SplitN(path, string(os.PathListSeparator), 2)[0]
	if !strings.HasPrefix(first, r.CacheRoot) || !strings.HasSuffix(path, "/usr/bin") {
		t.Errorf("PATH = %q; the pinned tool's cache dir must come first, ahead of /usr/bin", path)
	}
}

// A tool already at its pin on PATH is used as is: no install, no PATH change.
func TestResolver_PinnedToolOnPATHIsUsedAsIs(t *testing.T) {
	installed := false
	r := &Resolver{
		LookPath:  func(string) (string, error) { return "/bin/buf", nil },
		Probe:     func(string, Tool) string { return "1.50.0" },
		CacheRoot: t.TempDir(),
		Install:   func(context.Context, Tool, string, string) error { installed = true; return nil },
	}
	dir, err := r.Ensure(context.Background(), Tool{Binary: "buf", Version: "v1.50.0"})
	if err != nil || dir != "" || installed {
		t.Errorf("dir=%q err=%v installed=%v; a tool at its pin needs nothing", dir, err, installed)
	}
}

// If the pin cannot be provided (offline), the check is SKIPPED with the reason,
// never run against the wrong version and never PASS.
func TestRun_UnprovidableToolIsSkippedNotPassed(t *testing.T) {
	r := &Resolver{
		LookPath:  func(string) (string, error) { return "", exec.ErrNotFound },
		Probe:     func(string, Tool) string { return "" },
		CacheRoot: t.TempDir(),
		Install:   func(context.Context, Tool, string, string) error { return errors.New("network is unreachable") },
	}
	c := check("vuln-go", "govulncheck")
	c.Tools = []Tool{{Binary: "govulncheck", Version: "v1.7.0"}}
	fx := &fakeExec{}
	rep := Run(context.Background(), "checks", []Check{c}, Options{Out: io.Discard, Exec: fx.run, Resolver: r})
	if statuses(rep)["vuln-go"] != Skip || len(fx.ran) != 0 || !rep.Failed() {
		t.Errorf("rows=%+v ran=%v failed=%v", rep.Results, fx.ran, rep.Failed())
	}
	if !strings.Contains(rep.Results[0].Note, "network is unreachable") {
		t.Errorf("note %q should carry the reason", rep.Results[0].Note)
	}
}

// golangci-lint cannot analyze Go newer than the Go it was built with, so a
// PATH copy at the right VERSION built with an older Go is the wrong binary.
func TestResolver_RejectsRightVersionBuiltWithOlderGo(t *testing.T) {
	installed := false
	r := &Resolver{
		LookPath: func(string) (string, error) { return "/usr/local/bin/golangci-lint", nil },
		// A real probe reads the file, so a binary not installed yet has no
		// version.
		Probe: func(p string, _ Tool) string {
			if strings.HasPrefix(p, "/usr/") {
				return "v2.14.0"
			}
			if _, err := os.Stat(p); err != nil {
				return ""
			}
			return "v2.14.0"
		},
		BuiltWith: func(string) string { return "go1.26.2" },
		ModuleGo:  "1.27.0",
		Toolchain: func(context.Context) string { return "go1.27.2" },
		CacheRoot: filepath.Join(t.TempDir(), "c"),
		Install: func(_ context.Context, tool Tool, dir, _ string) error {
			installed = true
			return os.WriteFile(filepath.Join(dir, tool.Binary+exeSuffix()), nil, 0o755)
		},
	}
	// After install the cached copy is built with the project's Go.
	r.BuiltWith = func(p string) string {
		if strings.HasPrefix(p, r.CacheRoot) {
			return "go1.27.2"
		}
		return "go1.26.2"
	}
	dir, err := r.Ensure(context.Background(), Tool{Binary: "golangci-lint", Version: "v2.14.0", MatchGo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !installed || dir == "" {
		t.Errorf("installed=%v dir=%q: a linter built with go1.26 cannot lint go 1.27 code; the pin must be rebuilt", installed, dir)
	}
}

func TestGoLangAtLeast(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{
		{"go1.27.2", "1.27.0", true},
		{"go1.26.2", "1.27", false},
		{"go1.27.0", "1.27", true},
		{"go1.28.1", "1.27.0", true},
		{"garbage", "1.27", true}, // unreadable: let the tool say so itself
	} {
		if got := goLangAtLeast(c.have, c.want); got != c.ok {
			t.Errorf("goLangAtLeast(%q, %q) = %v, want %v", c.have, c.want, got, c.ok)
		}
	}
}

// The regression behind lookPathIn: exec.Command resolves the program against
// the PARENT's PATH, so a pinned tool placed first on the command's own PATH
// was silently ignored and the unpinned copy ran. The real exec path must find
// the binary where the command's PATH says it is.
func TestOSExec_ResolvesTheProgramOnTheCommandsPATH(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell script fixture")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pinned-tool-xyz"), []byte("#!/bin/sh\necho from-pinned-dir\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := osExec(context.Background(), t.TempDir(), []string{"PATH=" + dir}, []string{"pinned-tool-xyz"}, &out)
	if err != nil || !strings.Contains(out.String(), "from-pinned-dir") {
		t.Fatalf("err=%v out=%q: the program must be found on the PATH in env, not the parent's", err, out.String())
	}
	if err := osExec(context.Background(), dir, []string{"PATH=" + dir}, []string{"definitely-not-installed-xyz"}, &out); err == nil {
		t.Error("a missing program must be an error")
	}
}

// A leftover binary at the destination (a half-finished earlier install) must
// not block the new one: Windows cannot rename over an existing file.
func TestReplaceFile_ReplacesAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "new"), filepath.Join(dir, "old")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Errorf("dst = %q, want the new binary", b)
	}
	if err := replaceFile(filepath.Join(dir, "missing"), filepath.Join(dir, "fresh")); err == nil {
		t.Error("a missing source must be an error")
	}
}
