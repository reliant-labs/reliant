package lint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The bug these pin: `forge lint` ran whatever golangci-lint was on PATH.
// The workspace image's is a release binary built with go1.26.2, and every
// `go 1.27` module (control-plane, reliant) failed the gating lane with
// golangci-lint's own "the Go language version (go1.26) used to build
// golangci-lint is lower than the targeted Go version (1.27)" — no lint ran,
// on any project, and nothing said which binary or how to fix it.

// fakeToolchain is a golangciToolchain whose build-info answers come from
// maps, whose install writes a placeholder binary and whose runs replay
// canned output, so the decision logic runs without real toolchains,
// network, a minute-long build or a golangci-lint.
type fakeToolchain struct {
	t          *testing.T
	onPath     string            // "" = not on PATH
	builtWith  map[string]string // binary path → Go release it was built with
	versions   map[string]string // binary path → golangci-lint version
	toolchain  string            // what `go env GOVERSION` reports for the module
	installErr error
	installs   []string // "<goVersion> <version>" per install call
	notices    []string
	cache      string
	// outputs is what each binary prints when run; one not listed
	// reports a clean "0 issues.".
	outputs map[string]golangciOutput
	runs    []string // the binary of every run, in order
}

func newFakeToolchain(t *testing.T) *fakeToolchain {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "golangci-lint")
	if err := os.WriteFile(bin, []byte("placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeToolchain{
		t:         t,
		onPath:    bin,
		builtWith: map[string]string{bin: "go1.26.2"},
		versions:  map[string]string{},
		toolchain: "go1.27.0",
		cache:     t.TempDir(),
		outputs:   map[string]golangciOutput{},
	}
}

func (f *fakeToolchain) toolchainFor() golangciToolchain {
	return golangciToolchain{
		lookPath: func(string) (string, error) {
			if f.onPath == "" {
				return "", errors.New("not found")
			}
			return f.onPath, nil
		},
		readBuild: func(path string) (golangciBuild, error) {
			v, ok := f.builtWith[path]
			if !ok {
				return golangciBuild{}, errors.New("not a Go binary")
			}
			return golangciBuild{goVersion: v, version: f.versions[path]}, nil
		},
		moduleGoVersion: func(context.Context, string) (string, error) { return f.toolchain, nil },
		install: func(_ context.Context, gobin, goVersion, version string) error {
			f.installs = append(f.installs, goVersion+" "+version)
			if f.installErr != nil {
				return f.installErr
			}
			// The binary lands in a staging dir and is renamed into
			// place; record the version for its FINAL path.
			if filepath.Dir(gobin) != f.cache {
				f.t.Errorf("install must build into a staging dir under the cache, got %s", gobin)
			}
			if err := os.WriteFile(filepath.Join(gobin, golangciExeName()), []byte("built"), 0o755); err != nil {
				return err
			}
			final := filepath.Join(f.cache, version+"-"+goVersion, golangciExeName())
			f.builtWith[final] = goVersion
			f.versions[final] = version
			return nil
		},
		run: func(_ context.Context, bin string, args []string) golangciOutput {
			f.runs = append(f.runs, bin)
			out, ok := f.outputs[bin]
			if !ok {
				return golangciOutput{stdout: "0 issues.\n"}
			}
			// Under --issues-exit-code=0 every reported issue, typecheck
			// included, exits 0.
			if slices.Contains(args, "--issues-exit-code=0") {
				out.err = nil
			}
			return out
		},
		cacheDir: func() (string, error) { return f.cache, nil },
		pin:      "v2.14.0",
		notify:   func(msg string) { f.notices = append(f.notices, msg) },
	}
}

// runCtx is a lint run over the module at dir whose golangci-lint lanes see
// only this fake.
func (f *fakeToolchain) runCtx(dir string) *lintRunCtx {
	tc := f.toolchainFor()
	return &lintRunCtx{ctx: context.Background(), cwd: dir, paths: []string{"./..."}, golangciMemo: newGolangciMemo(tc)}
}

// moduleDir writes a go.mod with the given go directive.
func moduleDir(t *testing.T, goDirective string) string {
	t.Helper()
	dir := t.TempDir()
	body := "module example.com/m\n"
	if goDirective != "" {
		body += "\ngo " + goDirective + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestResolveGolangciLint_OlderThanModuleBuildsWithModuleToolchain is the
// control-plane case: PATH has a go1.26-built golangci-lint, the module is
// go 1.27. forge must lint with a golangci-lint built by the toolchain that
// compiles the module, not hand the module to a binary that refuses it.
func TestResolveGolangciLint_OlderThanModuleBuildsWithModuleToolchain(t *testing.T) {
	f := newFakeToolchain(t)
	dir := moduleDir(t, "1.27")

	res := resolveGolangciLint(context.Background(), dir, f.toolchainFor())
	if res.err != nil {
		t.Fatalf("resolution failed: %+v", res)
	}
	want := filepath.Join(f.cache, "v2.14.0-go1.27.0", golangciExeName())
	if res.path != want {
		t.Fatalf("linted with %q, want the forge build %q (the PATH binary %q cannot analyze go 1.27)", res.path, want, f.onPath)
	}
	if len(f.installs) != 1 || f.installs[0] != "go1.27.0 v2.14.0" {
		t.Fatalf("installs = %q, want one build of the pin with the module's toolchain", f.installs)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "go1.26.2") || !strings.Contains(f.notices[0], "go 1.27") {
		t.Fatalf("a one-time build must announce why it is happening; notices = %q", f.notices)
	}

	// A second run reuses the build: no second install.
	res2 := resolveGolangciLint(context.Background(), dir, f.toolchainFor())
	if res2.path != want || len(f.installs) != 1 {
		t.Fatalf("second resolution rebuilt or diverged: %+v installs=%q", res2, f.installs)
	}
	// The staging directory is gone; only the final binary remains.
	entries, _ := os.ReadDir(f.cache)
	if len(entries) != 1 || entries[0].Name() != "v2.14.0-go1.27.0" {
		t.Errorf("cache should hold only the finished build, got %v", entries)
	}
}

// TestResolveGolangciLint_CompatibleBinaryOnPathIsUsed keeps the common case
// free: no build, no network.
func TestResolveGolangciLint_CompatibleBinaryOnPathIsUsed(t *testing.T) {
	for _, tc := range []struct{ built, directive string }{
		{"go1.27.0", "1.27"},
		{"go1.27.0", "1.27.3"}, // only the language version matters
		{"go1.28rc1", "1.27"},
		{"go1.26.2", "1.26"},
	} {
		f := newFakeToolchain(t)
		f.builtWith[f.onPath] = tc.built
		res := resolveGolangciLint(context.Background(), moduleDir(t, tc.directive), f.toolchainFor())
		if res.path != f.onPath || res.err != nil || len(f.installs) != 0 {
			t.Errorf("built %s / go %s: got %+v installs=%q, want the PATH binary untouched", tc.built, tc.directive, res, f.installs)
		}
	}
}

// TestResolveGolangciLint_UnfixableMismatchNamesBothVersionsAndTheFix: when
// forge cannot build a compatible golangci-lint, the lane fails with a
// message a person can act on — never golangci-lint's bare exit 3.
func TestResolveGolangciLint_UnfixableMismatchNamesBothVersionsAndTheFix(t *testing.T) {
	f := newFakeToolchain(t)
	f.installErr = errors.New("dial tcp: lookup proxy.golang.org: no such host")

	res := resolveGolangciLint(context.Background(), moduleDir(t, "1.27"), f.toolchainFor())
	if res.err == nil {
		t.Fatalf("an incompatible golangci-lint that could not be replaced must fail the lane, got %+v", res)
	}
	msg := res.err.Error()
	for _, want := range []string{
		f.onPath,           // which binary
		"go1.26.2",         // what it was built with
		"go 1.27",          // what the module needs
		"proxy.golang.org", // why forge could not fix it
		"GOTOOLCHAIN=go1.27.0 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0", // the fix
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not name %q:\n%s", want, msg)
		}
	}
}

// TestResolveGolangciLint_ToolchainOlderThanModule covers GOTOOLCHAIN=local
// with an old `go`: building with it cannot help, so say so.
func TestResolveGolangciLint_ToolchainOlderThanModule(t *testing.T) {
	f := newFakeToolchain(t)
	f.toolchain = "go1.26.2"
	res := resolveGolangciLint(context.Background(), moduleDir(t, "1.27"), f.toolchainFor())
	if res.err == nil || len(f.installs) != 0 {
		t.Fatalf("want a failure without a pointless build, got %+v installs=%q", res, f.installs)
	}
	if !strings.Contains(res.err.Error(), "GOTOOLCHAIN=go1.27.0") {
		t.Errorf("the fix must name a toolchain that can build the module:\n%v", res.err)
	}
}

// TestResolveGolangciLint_NothingToCompare: no go directive, or a binary
// forge cannot read, defers to PATH.
func TestResolveGolangciLint_NothingToCompare(t *testing.T) {
	f := newFakeToolchain(t)
	if res := resolveGolangciLint(context.Background(), moduleDir(t, ""), f.toolchainFor()); res.path != f.onPath {
		t.Errorf("no go directive: want the PATH binary, got %+v", res)
	}

	f = newFakeToolchain(t)
	delete(f.builtWith, f.onPath) // a wrapper script, say
	if res := resolveGolangciLint(context.Background(), moduleDir(t, "1.27"), f.toolchainFor()); res.path != f.onPath || len(f.installs) != 0 {
		t.Errorf("unreadable build info: want the PATH binary, got %+v installs=%q", res, f.installs)
	}
}

// ── no golangci-lint on PATH ────────────────────────────────────────────────
//
// The bug these pin: reliant's CI runner had no golangci-lint, `forge lint`
// skipped the gating lane, and the job passed — golangci-lint had never run
// there. A missing golangci-lint must be provisioned like a mismatched one,
// or fail the lane; it must never be a skip.

// TestResolveGolangciLint_MissingBuildsThePin: no golangci-lint on PATH
// builds forge's pin with the module's toolchain, says so, and a second run
// reuses the build.
func TestResolveGolangciLint_MissingBuildsThePin(t *testing.T) {
	f := newFakeToolchain(t)
	f.onPath = ""
	dir := moduleDir(t, "1.27")

	res := resolveGolangciLint(context.Background(), dir, f.toolchainFor())
	want := filepath.Join(f.cache, "v2.14.0-go1.27.0", golangciExeName())
	if res.err != nil || res.path != want {
		t.Fatalf("no golangci-lint on PATH: want forge's pinned build at %s, got %+v", want, res)
	}
	if !slices.Equal(f.installs, []string{"go1.27.0 v2.14.0"}) {
		t.Errorf("want one build of the pin with the module's toolchain, got %q", f.installs)
	}
	if len(f.notices) != 1 || !strings.Contains(f.notices[0], "not on PATH") {
		t.Errorf("the one-time build must say why it happens, got %q", f.notices)
	}

	if again := resolveGolangciLint(context.Background(), dir, f.toolchainFor()); again.path != want || len(f.installs) != 1 {
		t.Errorf("a cached build must be reused, got %+v installs=%q", again, f.installs)
	}
}

// TestResolveGolangciLint_MissingAndUnbuildableFails: when forge cannot build
// the pin either, the resolution is a gating error naming the lane and both
// fixes — never a skip.
func TestResolveGolangciLint_MissingAndUnbuildableFails(t *testing.T) {
	f := newFakeToolchain(t)
	f.onPath = ""
	f.installErr = errors.New("dial tcp: proxy.golang.org: no route to host")

	res := resolveGolangciLint(context.Background(), moduleDir(t, "1.27"), f.toolchainFor())
	if res.err == nil {
		t.Fatalf("no golangci-lint and no build: want a gating error, got %+v", res)
	}
	for _, want := range []string{
		"golangci-lint lane cannot run", "no route to host", "forge tools install",
		"GOTOOLCHAIN=go1.27.0 go install " + golangciModulePath + "@v2.14.0",
	} {
		if !strings.Contains(res.err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, res.err)
		}
	}

	rc := f.runCtx(moduleDir(t, "1.27"))
	gate := findStep(t, "golangci-lint")
	fs, gated, err := collectGraded(t, gate, rc)
	if err != nil || !gated || len(fs) != 1 || fs[0].Severity != lintSevError {
		t.Errorf("gate: want one gating error finding, got gated=%v err=%v %+v", gated, err, fs)
	} else if !strings.Contains(fs[0].Message+fs[0].FixHint, "forge tools install") {
		t.Errorf("gate: want the fix named, got %+v", fs[0])
	}
}

// TestGolangciLaneRunsWithoutGolangciOnPath: the lane's shouldRun no longer
// skips when PATH has no golangci-lint — that skip is what let `forge lint`
// pass having run nothing.
func TestGolangciLaneRunsWithoutGolangciOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if run, skip := findStep(t, "golangci-lint").shouldRun(&lintRunCtx{}); !run {
		t.Fatalf("golangci-lint lane skipped with no golangci-lint on PATH (%q): a gating lane must never skip for a missing tool", skip)
	}
}

// TestGolangciLanesReportUnresolvedToolchain wires the resolution into the
// lanes: the gating lane fails with the actionable message (text and JSON),
// the advisory guardrail reports it could not run.
func TestGolangciLanesReportUnresolvedToolchain(t *testing.T) {
	unresolved := errors.New("golangci-lint at /usr/local/bin/golangci-lint was built with go1.26.2, but this module targets go 1.27")
	rc := &lintRunCtx{ctx: context.Background(), cwd: writeGuardConfig(t, t.TempDir()), paths: []string{"./..."},
		golangciMemo: &golangciMemo{resolved: true, res: golangciResolution{err: unresolved}}}

	gate := findStep(t, "golangci-lint")
	fs, gated, err := collectGraded(t, gate, rc)
	if err != nil || !gated || len(fs) != 1 || fs[0].Severity != lintSevError || !strings.Contains(fs[0].Message, "go 1.27") {
		t.Fatalf("json gate: want one gating error finding naming the mismatch, got gated=%v err=%v %+v", gated, err, fs)
	}

	guard := findStep(t, "typed-config guardrail")
	gfs, _, err := guard.collect(rc)
	if err != nil || len(gfs) == 0 || !isGapRule(gfs[0].Rule) {
		t.Fatalf("advisory guardrail: want a could-not-run finding, got err=%v %+v", err, gfs)
	}
}

// ── export data ─────────────────────────────────────────────────────────────
//
// The bug these pin: control-plane's `forge lint` failed its gating lane with
// a typecheck error in a file nobody had touched. The golangci-lint on PATH
// (v2.13.1, built with go1.27.0, golang.org/x/tools v0.49.0) passed the
// language check, since go1.27 speaks control-plane's `go 1.27.0`. But the
// module builds with go1.27.2, whose compiler writes export data version 5,
// and that x/tools decodes up to 4. Every package failed to import the
// standard library, and forge reported the result as a lint finding.

// exportSkewReport is that golangci-lint's whole report on control-plane,
// verbatim except for the toolchain directory: one typecheck error standing
// in for every package.
const exportSkewReport = `internal/billing/provideradapters/types.go:17:8: could not import time (/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.darwin-arm64/src/time/format.go:8:2: could not import errors (/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.darwin-arm64/src/errors/wrap.go:8:2: could not import internal/reflectlite (/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.27.2.darwin-arm64/src/internal/reflectlite/swapper.go:8:2: could not import internal/goarch (-: could not load export data: internal error in importing "internal/goarch" (cannot decode "internal/goarch", export data version 5 is greater than maximum supported version 4); please report an issue)))) (typecheck)
import "time"
       ^
1 issues:
* typecheck: 1
`

// newExportSkewFake is the control-plane case: PATH's golangci-lint speaks
// the module's language but cannot read the export data its toolchain
// writes. It returns the fake, the module and the path forge's own build
// lands at.
func newExportSkewFake(t *testing.T) (f *fakeToolchain, dir, pinned string) {
	t.Helper()
	f = newFakeToolchain(t)
	f.builtWith[f.onPath] = "go1.27.0"
	f.versions[f.onPath] = "v2.13.1"
	f.toolchain = "go1.27.2"
	f.outputs[f.onPath] = golangciOutput{stdout: exportSkewReport, err: errors.New("exit status 1")}
	dir = moduleDir(t, "1.27.0")
	writeGuardConfig(t, dir) // the guardrail lane runs only against a config that points forbidigo at env reads
	return f, dir, filepath.Join(f.cache, "v2.14.0-go1.27.2", golangciExeName())
}

// mentionsBogusReport reports whether any finding carries the unreadable
// binary's typecheck output.
func mentionsBogusReport(fs []lintJSONFinding) bool {
	for _, f := range fs {
		if strings.Contains(f.Message, "could not import") {
			return true
		}
	}
	return false
}

// TestGolangciExportDataSkew_LintsWithAToolchainBuild: the lane must not
// report what an unreadable run printed. It re-runs with forge's pinned
// golangci-lint built with the module's toolchain, says why in one line, and
// the rest of the lint run uses that build directly.
func TestGolangciExportDataSkew_LintsWithAToolchainBuild(t *testing.T) {
	f, dir, pinned := newExportSkewFake(t)
	rc := f.runCtx(dir)

	fs, gated, err := findStep(t, "golangci-lint").collect(rc)
	if err != nil || gated || len(fs) != 0 {
		t.Fatalf("golangci-lint at %s cannot read go1.27.2's export data, so its report is not a lint result; "+
			"the lane must re-run with one that can and report that run (clean). Got gated=%v err=%v findings=%+v",
			f.onPath, gated, err, fs)
	}
	if want := []string{f.onPath, pinned}; !slices.Equal(f.runs, want) {
		t.Fatalf("runs = %q, want the PATH binary, then forge's build %q", f.runs, want)
	}
	if want := []string{"go1.27.2 v2.14.0"}; !slices.Equal(f.installs, want) {
		t.Fatalf("installs = %q, want one build of the pin with the module's toolchain", f.installs)
	}
	if len(f.notices) != 1 {
		t.Fatalf("the switch must be explained in exactly one line; notices = %q", f.notices)
	}
	for _, want := range []string{f.onPath, "v2.13.1", "go1.27.0", "go1.27.2", "export data version 5; it reads up to 4", "v2.14.0"} {
		if !strings.Contains(f.notices[0], want) {
			t.Errorf("notice does not name %q:\n%s", want, f.notices[0])
		}
	}

	// The advisory guardrail in the same run goes straight to the build.
	gfs, _, gerr := collectTypedAccessGuardJSON(rc)
	if gerr != nil || mentionsBogusReport(gfs) {
		t.Fatalf("guardrail after the switch: err=%v findings=%+v", gerr, gfs)
	}
	if want := []string{f.onPath, pinned, pinned}; !slices.Equal(f.runs, want) {
		t.Fatalf("runs = %q, want the guardrail to reuse forge's build, not retry the PATH binary", f.runs)
	}
}

// TestGolangciExportDataSkew_ExitZero covers the gate and the guardrail
// lanes. The guardrail runs with --issues-exit-code=0, so there the bogus
// report EXITS 0 and would pass as a warning-level finding: detection must
// not depend on the exit status.
func TestGolangciExportDataSkew_ExitZero(t *testing.T) {
	f, dir, pinned := newExportSkewFake(t)
	if fs, gated, err := findStep(t, "golangci-lint").collect(f.runCtx(dir)); err != nil || gated {
		t.Fatalf("gate: want the re-run's clean verdict, got gated=%v err=%v %+v", gated, err, fs)
	}
	if want := []string{f.onPath, pinned}; !slices.Equal(f.runs, want) {
		t.Fatalf("gate runs = %q, want %q", f.runs, want)
	}

	f, dir, pinned = newExportSkewFake(t)
	gfs, gated, err := collectTypedAccessGuardJSON(f.runCtx(dir))
	if err != nil || gated || mentionsBogusReport(gfs) {
		t.Fatalf("json guardrail reported the unreadable run (it exits 0 under --issues-exit-code=0): gated=%v err=%v findings=%+v", gated, err, gfs)
	}
	if want := []string{f.onPath, pinned}; !slices.Equal(f.runs, want) {
		t.Fatalf("json guardrail runs = %q, want %q", f.runs, want)
	}

}

// TestGolangciExportDataSkew_PinCannotReadEitherFailsWithRunbook is the
// future-toolchain case: forge's own build is too old as well. The lane fails
// with one finding that names both binaries and the toolchain, never with
// either run's typecheck report, and the guardrail does not try again.
func TestGolangciExportDataSkew_PinCannotReadEitherFailsWithRunbook(t *testing.T) {
	f, dir, pinned := newExportSkewFake(t)
	f.outputs[pinned] = f.outputs[f.onPath]
	rc := f.runCtx(dir)

	fs, gated, err := collectGraded(t, findStep(t, "golangci-lint"), rc)
	if err != nil || !gated || len(fs) != 1 || mentionsBogusReport(fs) {
		t.Fatalf("want exactly one gating finding, the runbook; got gated=%v err=%v findings=%+v", gated, err, fs)
	}
	msg := fs[0].Message
	for _, want := range []string{
		f.onPath, "go1.27.0", // the binary on PATH and its build
		"go1.27.2", // the toolchain whose export data neither reads
		"export data version 5; it reads up to 4", // what is wrong
		pinned, "v2.14.0", // forge's build, which reads it no better
		"golangci-lint@latest", "forge's pin needs a bump", // the fix
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("runbook does not name %q:\n%s", want, msg)
		}
	}

	runs := len(f.runs)
	gfs, _, _ := collectTypedAccessGuardJSON(rc)
	if len(f.runs) != runs || len(gfs) != 1 || !strings.Contains(gfs[0].Message, "export data") {
		t.Fatalf("guardrail must reuse the verdict without running anything: runs %d→%d findings=%+v", runs, len(f.runs), gfs)
	}
}

// TestGolangciExportDataSkew_UnbuildablePinNamesTheFix: offline, the build
// cannot happen; the runbook says what to install and where.
func TestGolangciExportDataSkew_UnbuildablePinNamesTheFix(t *testing.T) {
	f, dir, _ := newExportSkewFake(t)
	f.installErr = errors.New("dial tcp: lookup proxy.golang.org: no such host")

	fs, gated, _ := collectGraded(t, findStep(t, "golangci-lint"), f.runCtx(dir))
	if !gated || len(fs) != 1 || mentionsBogusReport(fs) {
		t.Fatalf("want one gating runbook finding, got gated=%v %+v", gated, fs)
	}
	for _, want := range []string{
		f.onPath, "go1.27.0", "go1.27.2", "proxy.golang.org",
		"GOTOOLCHAIN=go1.27.2 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0",
		"ahead of " + filepath.Dir(f.onPath) + " on PATH",
	} {
		if !strings.Contains(fs[0].Message, want) {
			t.Errorf("runbook does not name %q:\n%s", want, fs[0].Message)
		}
	}
	if want := []string{f.onPath}; !slices.Equal(f.runs, want) {
		t.Errorf("runs = %q, want only the failed PATH run", f.runs)
	}
}

// TestGolangciExportDataSkew_ThePinIsNotRebuilt: every build of one
// golangci-lint version carries the same x/tools, so when the binary that
// cannot read the export data IS the pin, built with an older Go, rebuilding
// it with the module's toolchain would cost a minute and change nothing.
func TestGolangciExportDataSkew_ThePinIsNotRebuilt(t *testing.T) {
	f, dir, _ := newExportSkewFake(t)
	f.versions[f.onPath] = "v2.14.0"

	fs, gated, _ := collectGraded(t, findStep(t, "golangci-lint"), f.runCtx(dir))
	if !gated || len(fs) != 1 || !strings.Contains(fs[0].Message, "golangci-lint@latest") {
		t.Fatalf("want the runbook for a pin that is too old, got gated=%v %+v", gated, fs)
	}
	if len(f.installs) != 0 {
		t.Errorf("rebuilt the pin, which cannot help: installs = %q", f.installs)
	}
}

func TestDetectExportDataSkew(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         string // exportDataSkew.String(); "" = no skew
	}{
		{"unified format, control-plane's report", exportSkewReport, "export data version 5; it reads up to 4"},
		{"indexed format", `x.go:3:2: could not import fmt (cannot import "fmt" (unknown iexport format version 3), export data is newer version - update tool)`, "unknown iexport format version 3"},
		// A dependency that failed to compile has no export data either.
		// That is a real error, and must reach the user.
		{"broken dependency", `x.go:3:2: could not import example.com/broken (-: could not load export data: no export data for "example.com/broken")`, ""},
		{"clean", "0 issues.\n", ""},
	} {
		got, found := detectExportDataSkew(tc.output)
		if found != (tc.want != "") || (found && got.String() != tc.want) {
			t.Errorf("%s: got %q found=%v, want %q", tc.name, got.String(), found, tc.want)
		}
	}
}

// TestReadGolangciBuild reads this test binary's own build info: the export
// data reader is golang.org/x/tools, which the lint package links.
func TestReadGolangciBuild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	b, err := readGolangciBuild(exe)
	if err != nil {
		t.Fatal(err)
	}
	if b.goVersion != goRelease(runtime.Version()) || b.xtools == "" {
		t.Errorf("read %+v, want built with %s and a golang.org/x/tools version", b, runtime.Version())
	}
	if got := goRelease("go1.27.2 X:nodwarf5"); got != "go1.27.2" {
		t.Errorf("goRelease kept the experiment suffix: %q", got)
	}
}
