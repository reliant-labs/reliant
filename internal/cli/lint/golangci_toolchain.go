package lint

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/toolcache"
)

// A golangci-lint can be unable to analyze a module for two reasons that
// have nothing to do with the code. Both used to reach the user as lint
// failures, and neither named the binary or the fix.
//
// # The Go language version
//
// golangci-lint can only analyze code written for a Go language version no
// newer than the Go it was BUILT with (its go/types). Run against a newer
// module it stops before linting anything:
//
//	can't load config: the Go language version (go1.26) used to build
//	golangci-lint is lower than the targeted Go version (1.27)
//
// That is what `forge lint` hit on control-plane and reliant (both `go 1.27`):
// the workspace image ships a golangci-lint release binary built with
// go1.26.2. The binary records the Go it was built with, so
// resolveGolangciLint decides this up front, per module:
//
//  1. Use golangci-lint from PATH when it was built with a Go at least as
//     new as the module's go directive (the common case: no work, no
//     network).
//  2. Otherwise build forge's pinned golangci-lint with the very toolchain
//     `go` selects for this module, into a forge-owned cache keyed by both
//     versions, and lint with that. It is built once per (pin, toolchain).
//  3. If that build fails (offline, no toolchain), fail the lane with a
//     message naming the binary, both Go versions and the command that fixes
//     it — not golangci-lint's own error, which names neither the binary nor
//     the remedy.
//
// # The export data
//
// golangci-lint type-checks a package's imports from the export data the
// active toolchain's compiler writes, and decodes it with the
// golang.org/x/tools it was built against — not with the Go it was built
// with. When the compiler writes a newer format than that x/tools reads,
// every package "fails" typecheck:
//
//	could not import internal/goarch (-: could not load export data: internal
//	error in importing "internal/goarch" (cannot decode "internal/goarch",
//	export data version 5 is greater than maximum supported version 4); ...
//
// go1.27.2 writes version 5. golangci-lint v2.13.1 (x/tools v0.49.0) reads up
// to 4 whichever Go builds it, and v2.14.0 (x/tools v0.50.0) reads 5 even when
// built with go1.27.0 — both measured on control-plane. So comparing build Go
// versions, at any precision, answers the wrong question: it would rebuild
// binaries that work and keep ones that do not. Neither binary states the
// format, so forge asks the binary: execGolangci reads every run's output for
// that report, and on it
//
//  1. discards the run — its output is one bogus typecheck error standing for
//     every package, and nothing was linted;
//  2. re-runs with forge's pinned golangci-lint built with the module's
//     toolchain (built once, as above), after one line saying why. Later
//     lanes of the same `forge lint` go straight to that binary;
//  3. fails the lane with a runbook naming the binary, its build, the
//     toolchain and the command, when the pin cannot read it either or
//     cannot be built.
//
// Asking costs nothing when the binary is fine. When it is not, the failing
// run stops at package loading (~3s on control-plane, whose full run is
// ~50s), and the retry reuses the export data that run compiled.

// golangciModulePath is the v2 module forge installs golangci-lint from.
const golangciModulePath = "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"

// # No golangci-lint at all
//
// A machine with no golangci-lint on PATH used to SKIP the gating lane, and
// `forge lint` still exited 0. reliant's CI lint job had never run
// golangci-lint: its runner had none, forge skipped the lane, and the job was
// green. A gating lane that did not run is not a lane that passed, so the
// missing case is provisioned exactly like the mismatch case: forge builds
// its pinned golangci-lint with the module's toolchain into the same cache
// (once per pin and toolchain), and when it cannot, the lane FAILS with the
// command that fixes it (provisionMissingGolangci).

// golangciResolution is the outcome of choosing a golangci-lint for a module.
type golangciResolution struct {
	// path is the binary to run. Empty when none is available.
	path string
	// err is a gating failure: no golangci-lint that can analyze this
	// module exists, and forge could not provision one.
	err error
}

// golangciBuild is what a golangci-lint binary's embedded build info says
// about it. Empty fields are unknown.
type golangciBuild struct {
	// goVersion is the Go release it was built with ("go1.27.0"): the
	// newest Go language its go/types understands.
	goVersion string
	// version is the golangci-lint module version ("v2.13.1"; "(devel)"
	// for a build from a checkout).
	version string
	// xtools is its golang.org/x/tools ("v0.49.0"), whose importer decodes
	// the toolchain's export data.
	xtools string
}

// String is the description the notices print: "v2.13.1, built with
// go1.27.0, golang.org/x/tools v0.49.0", leaving out what is unknown.
func (b golangciBuild) String() string {
	var parts []string
	if b.version != "" {
		parts = append(parts, b.version)
	}
	if b.goVersion != "" {
		parts = append(parts, "built with "+b.goVersion)
	}
	if b.xtools != "" {
		parts = append(parts, "golang.org/x/tools "+b.xtools)
	}
	return strings.Join(parts, ", ")
}

// describeAt names a binary for a message: its path, then its build when
// known.
func describeAt(path string, b golangciBuild) string {
	if s := b.String(); s != "" {
		return path + " (" + s + ")"
	}
	return path
}

// golangciOutput is one golangci-lint run, captured.
type golangciOutput struct {
	stdout, stderr string
	// err is how the process exited: nil for status 0.
	err error
}

func (o golangciOutput) combined() string { return o.stdout + o.stderr }

// golangciToolchain is the outside world resolveGolangciLint and
// execGolangci consult. The zero value is not usable;
// productionGolangciToolchain wires the real one, tests substitute fakes.
type golangciToolchain struct {
	lookPath func(file string) (string, error)
	// readBuild reads a binary's embedded build info. It fails for a file
	// that is not a Go executable (a wrapper script, a stripped build).
	readBuild func(path string) (golangciBuild, error)
	// moduleGoVersion reports the toolchain `go` selects for the module
	// at dir ("go1.27.0") — the one that compiles it, so the one whose
	// language and export data golangci-lint must understand.
	moduleGoVersion func(ctx context.Context, dir string) (string, error)
	// install builds golangci-lint@version with toolchain goVersion into
	// gobin.
	install func(ctx context.Context, gobin, goVersion, version string) error
	// run executes a golangci-lint binary with args, capturing its output.
	run      func(ctx context.Context, bin string, args []string) golangciOutput
	cacheDir func() (string, error)
	pin      string
	// notify receives the one line printed when forge lints with its own
	// build instead of the one on PATH, so neither a minute-long first
	// build nor the switch itself goes unexplained.
	notify func(msg string)
}

func productionGolangciToolchain() golangciToolchain {
	return golangciToolchain{
		lookPath:        exec.LookPath,
		readBuild:       readGolangciBuild,
		moduleGoVersion: moduleToolchainVersion,
		install:         installGolangciLint,
		run:             runGolangciBinary,
		cacheDir:        golangciCacheDir,
		pin:             templates.GolangciLintVersion,
		notify:          func(msg string) { fmt.Fprintln(os.Stderr, msg) },
	}
}

// golangciMemo is one run's golangci-lint resolution, shared by every lane
// that runs golangci-lint so they agree on the binary and pay for at most
// one provisioning build. A pointer, so the per-step scoped copies the
// structured driver makes share it; the run's constructor sets it.
type golangciMemo struct {
	mu       sync.Mutex
	resolved bool
	res      golangciResolution
	// tc is the outside world, injected by newGolangciMemo: production
	// passes productionGolangciToolchain(), tests pass a fake.
	tc golangciToolchain
}

func newGolangciMemo(tc golangciToolchain) *golangciMemo {
	return &golangciMemo{tc: tc}
}

func (m *golangciMemo) toolchain() golangciToolchain { return m.tc }

func (rc *lintRunCtx) memo() *golangciMemo {
	if rc.golangciMemo == nil {
		rc.golangciMemo = newGolangciMemo(productionGolangciToolchain())
	}
	return rc.golangciMemo
}

// moduleDir is the directory whose module this run lints.
func (rc *lintRunCtx) moduleDir() string {
	if rc.cwd != "" {
		return rc.cwd
	}
	dir, _ := os.Getwd()
	return dir
}

// goPackages is rc.paths with any package under a node_modules directory
// excluded (goexec.ExcludeNodeModules): the Go package patterns every lane
// that shells golangci-lint passes it.
func (rc *lintRunCtx) goPackages() []string {
	return goexec.ExcludeNodeModules(rc.runContext(), rc.moduleDir(), nil, rc.paths)
}

func (rc *lintRunCtx) runContext() context.Context {
	if rc.ctx == nil {
		return context.Background()
	}
	return rc.ctx
}

// golangci resolves the golangci-lint binary for the module this run lints,
// once per run.
func (rc *lintRunCtx) golangci() golangciResolution {
	m := rc.memo()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.resolved {
		m.res = resolveGolangciLint(rc.runContext(), rc.moduleDir(), m.toolchain())
		m.resolved = true
	}
	return m.res
}

// execGolangci runs this module's golangci-lint with args and returns what
// it printed. It is the only way forge runs golangci-lint, so it is the one
// place a binary that cannot read the toolchain's export data is caught (see
// the file comment): that run is discarded, and args run again with a binary
// that can, which the rest of this lint run then uses. A non-nil error means
// no golangci-lint could analyze the module. It is the runbook, and the lane
// reports it in place of any output.
func (rc *lintRunCtx) execGolangci(args []string) (golangciOutput, error) {
	res := rc.golangci()
	if res.err != nil {
		return golangciOutput{}, res.err
	}
	m := rc.memo()
	tc := m.toolchain()
	ctx := rc.runContext()
	out := tc.run(ctx, res.path, args)
	skew, found := detectExportDataSkew(out.combined())
	if !found {
		return out, nil
	}
	next, out := retryAfterExportDataSkew(ctx, rc.moduleDir(), tc, res.path, skew, args)
	m.mu.Lock()
	m.res = next
	m.mu.Unlock()
	if next.err != nil {
		return golangciOutput{}, next.err
	}
	return out, nil
}

// resolveGolangciLint picks the golangci-lint that can analyze the module at
// dir. See the file comment for the three outcomes.
func resolveGolangciLint(ctx context.Context, dir string, tc golangciToolchain) golangciResolution {
	onPath, err := tc.lookPath("golangci-lint")
	if err != nil {
		return provisionMissingGolangci(ctx, dir, tc)
	}

	// The language the module is written in, from go.mod. No go.mod (or
	// no go directive) means there is no version to mismatch.
	modLang := moduleGoDirective(dir)
	if modLang == "" {
		return golangciResolution{path: onPath}
	}

	build, err := tc.readBuild(onPath)
	builtWith := build.goVersion
	if err != nil || !version.IsValid(builtWith) {
		// Not a Go binary forge can read (a wrapper script, a stripped
		// build). It may well work; golangci-lint reports a real mismatch
		// itself.
		return golangciResolution{path: onPath}
	}
	if langAtLeast(builtWith, modLang) {
		return golangciResolution{path: onPath}
	}

	// Mismatch. Build the pin with the toolchain that compiles this module.
	toolchain, err := tc.moduleGoVersion(ctx, dir)
	if err != nil || !version.IsValid(toolchain) {
		return golangciResolution{err: golangciMismatchError(onPath, builtWith, modLang, "", tc.pin,
			fmt.Errorf("could not determine the Go toolchain for this module: %w", errOrInvalid(err, toolchain)))}
	}
	if !langAtLeast(toolchain, modLang) {
		return golangciResolution{err: golangciMismatchError(onPath, builtWith, modLang, toolchain, tc.pin,
			fmt.Errorf("the selected Go toolchain %s is itself older than the module's go %s", toolchain, modLang))}
	}

	root, gobin, built, err := tc.pinBuild(toolchain)
	if err != nil {
		return golangciResolution{err: golangciMismatchError(onPath, builtWith, modLang, toolchain, tc.pin, err)}
	}
	usable := func() bool {
		b, berr := tc.readBuild(built)
		return berr == nil && langAtLeast(b.goVersion, modLang)
	}
	if err := installAtomically(ctx, tc, root, gobin, toolchain, usable, func() {
		tc.notify(fmt.Sprintf("🔧 golangci-lint at %s was built with %s, older than this module's go %s — building golangci-lint %s with %s into %s (one time)…",
			onPath, builtWith, modLang, tc.pin, toolchain, gobin))
	}); err != nil {
		return golangciResolution{err: golangciMismatchError(onPath, builtWith, modLang, toolchain, tc.pin, err)}
	}
	if !usable() {
		return golangciResolution{err: golangciMismatchError(onPath, builtWith, modLang, toolchain, tc.pin,
			fmt.Errorf("the build produced no usable binary at %s", built))}
	}
	return golangciResolution{path: built}
}

// provisionMissingGolangci resolves a module whose machine has no
// golangci-lint on PATH: forge's pinned build with the module's toolchain,
// reused from the cache or built into it, and otherwise a gating failure
// naming the fix. Never a skip (see the file comment).
func provisionMissingGolangci(ctx context.Context, dir string, tc golangciToolchain) golangciResolution {
	modLang := moduleGoDirective(dir)
	toolchain, err := tc.moduleGoVersion(ctx, dir)
	if err != nil || !version.IsValid(toolchain) {
		return golangciResolution{err: golangciMissingError(tc.pin, "", modLang,
			fmt.Errorf("could not determine the Go toolchain for this module: %w", errOrInvalid(err, toolchain)))}
	}
	root, gobin, built, err := tc.pinBuild(toolchain)
	if err != nil {
		return golangciResolution{err: golangciMissingError(tc.pin, toolchain, modLang, err)}
	}
	usable := func() bool {
		b, berr := tc.readBuild(built)
		return berr == nil && (modLang == "" || langAtLeast(b.goVersion, modLang))
	}
	if err := installAtomically(ctx, tc, root, gobin, toolchain, usable, func() {
		tc.notify(fmt.Sprintf("🔧 golangci-lint is not on PATH — building forge's pinned golangci-lint %s with %s into %s (one time)…",
			tc.pin, toolchain, gobin))
	}); err != nil {
		return golangciResolution{err: golangciMissingError(tc.pin, toolchain, modLang, err)}
	}
	if !usable() {
		return golangciResolution{err: golangciMissingError(tc.pin, toolchain, modLang,
			fmt.Errorf("the build produced no usable binary at %s", built))}
	}
	return golangciResolution{path: built}
}

// golangciMissingError is the failure of a gating lane that could not run
// because there is no golangci-lint: it names the lane, why forge could not
// build one, and both commands that fix it.
func golangciMissingError(pin, toolchain, modLang string, cause error) error {
	fixTC := toolchain
	if fixTC == "" && modLang != "" {
		fixTC = releaseFor(modLang)
	}
	install := "go install " + golangciModulePath + "@" + pin
	if fixTC != "" {
		install = "GOTOOLCHAIN=" + fixTC + " " + install
	}
	return fmt.Errorf("golangci-lint is not on PATH, so the golangci-lint lane cannot run — and a gating lane that did not run cannot pass. "+
		"forge could not build its pinned golangci-lint %s: %v. "+
		"Fix: run `forge tools install golangci-lint` (it installs golangci-lint %s), or `%s` with its GOBIN on PATH, then re-run `forge lint`",
		pin, cause, pin, install)
}

// pinBuild locates forge's build of its pinned golangci-lint with toolchain:
// root is the tools cache, gobin the build's own directory (one per pin and
// toolchain), binary the executable in it.
func (tc golangciToolchain) pinBuild(toolchain string) (root, gobin, binary string, err error) {
	root, err = tc.cacheDir()
	if err != nil {
		return "", "", "", err
	}
	gobin = filepath.Join(root, tc.pin+"-"+toolchain)
	return root, gobin, filepath.Join(gobin, golangciExeName()), nil
}

// exportDataSkew is golangci-lint reporting that it cannot decode the export
// data the toolchain's compiler wrote.
type exportDataSkew struct {
	// report is the matched text.
	report string
	// written and readable are the format versions ("5", "4"), when the
	// report states them.
	written, readable string
}

func (s exportDataSkew) String() string {
	if s.readable != "" {
		return fmt.Sprintf("export data version %s; it reads up to %s", s.written, s.readable)
	}
	return s.report
}

// How golang.org/x/tools' importers report a format newer than they decode:
// the unified format (internal/pkgbits, the compiler's since Go 1.20) and the
// indexed format before it (internal/gcimporter). Only these. A bare "could
// not load export data" also covers a dependency that failed to compile,
// which is a real error the user must see.
var (
	unifiedExportSkew = regexp.MustCompile(`export data version (\d+) is greater than maximum supported version (\d+)`)
	indexedExportSkew = regexp.MustCompile(`unknown iexport format version \d+`)
)

// detectExportDataSkew reports whether a golangci-lint run's output says the
// binary cannot read the toolchain's export data.
func detectExportDataSkew(output string) (exportDataSkew, bool) {
	if m := unifiedExportSkew.FindStringSubmatch(output); m != nil {
		return exportDataSkew{report: m[0], written: m[1], readable: m[2]}, true
	}
	if m := indexedExportSkew.FindString(output); m != "" {
		return exportDataSkew{report: m}, true
	}
	return exportDataSkew{}, false
}

// errPinTooOld is the export-data failure no build forge can make repairs:
// every build of one golangci-lint version carries the same x/tools, so when
// the pin is what cannot read the toolchain's export data, rebuilding it
// with another Go changes nothing.
var errPinTooOld = errors.New("forge's pinned golangci-lint cannot read it either")

// retryAfterExportDataSkew runs args again with a golangci-lint that can
// read the export data failed could not (see the file comment). It returns
// the binary the rest of the run should use, with that run's output. A
// resolution error means no such binary could be had, and the output is
// then empty — never the bogus typecheck report.
func retryAfterExportDataSkew(ctx context.Context, dir string, tc golangciToolchain, failed string, skew exportDataSkew, args []string) (golangciResolution, golangciOutput) {
	failedBuild, _ := tc.readBuild(failed)
	fail := func(toolchain string, cause error) (golangciResolution, golangciOutput) {
		return golangciResolution{err: exportDataSkewError(failed, failedBuild, toolchain, moduleGoDirective(dir), tc.pin, skew, cause)},
			golangciOutput{}
	}

	toolchain, err := tc.moduleGoVersion(ctx, dir)
	if err != nil || !version.IsValid(toolchain) {
		return fail("", fmt.Errorf("could not determine the Go toolchain for this module: %w", errOrInvalid(err, toolchain)))
	}
	root, gobin, pinned, err := tc.pinBuild(toolchain)
	if err != nil {
		return fail(toolchain, err)
	}
	if failed == pinned || failedBuild.version == tc.pin {
		return fail(toolchain, errPinTooOld)
	}

	why := fmt.Sprintf("🔧 golangci-lint at %s cannot read the export data %s writes (%s) — linting with forge's golangci-lint %s built with %s",
		describeAt(failed, failedBuild), toolchain, skew, tc.pin, toolchain)
	usable := func() bool {
		_, berr := tc.readBuild(pinned)
		return berr == nil
	}
	built := false
	if err := installAtomically(ctx, tc, root, gobin, toolchain, usable, func() {
		built = true
		tc.notify(why + ", building it into " + gobin + " (one time)…")
	}); err != nil {
		return fail(toolchain, err)
	}
	if !built {
		tc.notify(why + "; upgrade " + failed + " to skip this retry")
	}
	if !usable() {
		return fail(toolchain, fmt.Errorf("the build produced no usable binary at %s", pinned))
	}

	out := tc.run(ctx, pinned, args)
	if again, found := detectExportDataSkew(out.combined()); found {
		pinnedBuild, _ := tc.readBuild(pinned)
		return fail(toolchain, fmt.Errorf("%w: %s reports %s", errPinTooOld, describeAt(pinned, pinnedBuild), again))
	}
	return golangciResolution{path: pinned}, out
}

// exportDataSkewError is the runbook for a golangci-lint that cannot read
// the toolchain's export data when forge could not supply one that can: the
// binary and its build, the toolchain, why forge could not fix it, and the
// command that does.
func exportDataSkewError(failed string, failedBuild golangciBuild, toolchain, modLang, pin string, skew exportDataSkew, cause error) error {
	writer := toolchain
	if writer == "" {
		writer = "this module's Go toolchain"
	}
	fixTC := toolchain
	if fixTC == "" && modLang != "" {
		fixTC = releaseFor(modLang)
	}
	install := func(ver string) string {
		cmd := "go install " + golangciModulePath + "@" + ver
		if fixTC != "" {
			cmd = "GOTOOLCHAIN=" + fixTC + " " + cmd
		}
		return cmd
	}
	fix := fmt.Sprintf("run %#q and put its GOBIN ahead of %s on PATH", install(pin), filepath.Dir(failed))
	if errors.Is(cause, errPinTooOld) {
		fix = fmt.Sprintf("put a golangci-lint newer than %s, built against a golang.org/x/tools that reads %s's export data, ahead of %s on PATH (%#q), "+
			"or build this module with an older toolchain; forge's pin needs a bump either way",
			pin, writer, filepath.Dir(failed), install("latest"))
	}
	return fmt.Errorf("golangci-lint at %s cannot read the export data %s writes (%s), so every package would fail typecheck and nothing would be linted. "+
		"forge could not provide a golangci-lint that can: %v. Fix: %s",
		describeAt(failed, failedBuild), writer, skew, cause, fix)
}

// installAtomically resolves forge's golangci-lint build at gobin through the
// shared tool cache (internal/toolcache): a usable build is used as is, and a
// missing or unusable one is built into a staging directory that lands by one
// rename, so concurrent `forge lint` runs (parallel CI jobs, agents in other
// worktrees) never exec a half-written binary. announce runs only when this run builds.
func installAtomically(ctx context.Context, tc golangciToolchain, root, gobin, toolchain string, usable func() bool, announce func()) error {
	_, err := toolcache.Ensure(ctx, root, filepath.Base(gobin), func(string) bool { return usable() }, func(ctx context.Context, stage string) error {
		announce()
		return tc.install(ctx, stage, toolchain, tc.pin)
	})
	return err
}

// golangciMismatchError is the actionable failure: which binary, which two
// Go versions disagree, why forge could not fix it, and the command that
// does.
func golangciMismatchError(onPath, builtWith, modLang, toolchain, pin string, cause error) error {
	// The fix must build with a toolchain that speaks the module's
	// language: the selected one when it does, else the first release
	// that does (GOTOOLCHAIN downloads it).
	fixTC := toolchain
	if fixTC == "" || !langAtLeast(fixTC, modLang) {
		fixTC = releaseFor(modLang)
	}
	return fmt.Errorf("golangci-lint at %s was built with %s, but this module targets go %s (go.mod) — "+
		"golangci-lint cannot analyze Go newer than the Go it was built with. "+
		"forge could not build golangci-lint %s for it: %v. "+
		"Fix: run `GOTOOLCHAIN=%s go install %s@%s` and put its GOBIN ahead of %s on PATH",
		onPath, builtWith, modLang, pin, cause, fixTC, golangciModulePath, pin, filepath.Dir(onPath))
}

// releaseFor names the first toolchain release that speaks language
// version lang: "1.27" → "go1.27.0" (GOTOOLCHAIN wants a release, and
// "go1.27" is not one); "1.27.3" → "go1.27.3".
func releaseFor(lang string) string {
	if strings.Count(lang, ".") == 1 {
		return "go" + lang + ".0"
	}
	return "go" + lang
}

func errOrInvalid(err error, got string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("unrecognised version %q", got)
}

// langAtLeast reports whether Go release have speaks at least language
// version want ("1.27", "1.27.0" or "go1.27"). Only the language (major.minor)
// matters: that is the check golangci-lint itself makes.
func langAtLeast(have, want string) bool {
	if !strings.HasPrefix(want, "go") {
		want = "go" + want
	}
	return version.Compare(version.Lang(have), version.Lang(want)) >= 0
}

// moduleGoDirective returns the go directive of the module enclosing dir
// ("1.27"), or "" when there is none.
func moduleGoDirective(dir string) string {
	for d := dir; ; {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil {
			f, perr := modfile.ParseLax("go.mod", data, nil)
			if perr != nil || f.Go == nil {
				return ""
			}
			return f.Go.Version
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// goRelease trims what a Go version string carries after the release — the
// " X:<experiment>" suffix of a toolchain built with GOEXPERIMENT — so
// "go1.27.2 X:nodwarf5" compares as "go1.27.2".
func goRelease(v string) string {
	if fields := strings.Fields(v); len(fields) > 0 {
		return fields[0]
	}
	return v
}

// readGolangciBuild reads a golangci-lint binary's embedded build info — no
// subprocess, works for any Go executable.
func readGolangciBuild(path string) (golangciBuild, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return golangciBuild{}, err
	}
	b := golangciBuild{goVersion: goRelease(info.GoVersion), version: info.Main.Version}
	for _, dep := range info.Deps {
		if dep.Path != "golang.org/x/tools" {
			continue
		}
		if dep.Replace != nil {
			dep = dep.Replace
		}
		b.xtools = dep.Version
	}
	return b, nil
}

// moduleToolchainVersion asks `go` which toolchain it runs for the module at
// dir — after GOTOOLCHAIN switching, so a module that requires a newer Go
// reports (and downloads) that one.
func moduleToolchainVersion(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "env", "GOVERSION")
	cmd.Dir = dir
	cmd.Env = goexec.Env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOVERSION: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return goRelease(strings.TrimSpace(string(out))), nil
}

// installGolangciLint builds golangci-lint@ver with exactly toolchain
// goVersion into gobin. It runs from gobin (a fresh staging directory),
// outside any module, so the project's go.mod and go.work cannot steer the
// build.
func installGolangciLint(ctx context.Context, gobin, goVersion, ver string) error {
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "go", "install", golangciModulePath+"@"+ver)
	cmd.Dir = gobin
	cmd.Env = goexec.WithTrimpath(goexec.Env("GOBIN="+gobin, "GOTOOLCHAIN="+goVersion, "GOWORK=off", "GOFLAGS="))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(out.String())
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

// runGolangciBinary runs bin with args and captures its output. It is
// captured rather than streamed because a run may turn out to be the bogus
// report of a binary that cannot read the export data, which must never
// reach the user.
func runGolangciBinary(ctx context.Context, bin string, args []string) golangciOutput {
	cmd := exec.CommandContext(ctx, bin, args...)
	// golangci-lint compiles the export data it type-checks against through
	// `go list -export`: -trimpath lets that reuse the cache other worktrees
	// of the module filled (goexec.WithTrimpath).
	cmd.Env = goexec.WithTrimpath(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return golangciOutput{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// golangciCacheDir is where forge keeps the golangci-lint builds it makes: its
// kind directory in the shared tool cache (FORGE_TOOLS_CACHE relocates it).
func golangciCacheDir() (string, error) {
	return toolcache.KindDir(toolcache.KindGolangci)
}

func golangciExeName() string {
	if filepath.Separator == '\\' {
		return "golangci-lint.exe"
	}
	return "golangci-lint"
}

// golangciUnresolvedFinding is the --json / --quiet form of a golangci-lint
// that cannot analyze this module and could not be replaced: the actionable
// message rather than golangci-lint's own output. rule is the lane's
// `-unavailable` id, so the lane is reported as one that could NOT run and
// graded by applyGapPolicy (lint_skip.go) — a failure for the gating lane.
func golangciUnresolvedFinding(err error, rule string) lintJSONFinding {
	return lintJSONFinding{
		Severity: lintSevWarning,
		Rule:     rule,
		Message:  err.Error(),
		FixHint:  golangciUnresolvedFix,
	}
}

// golangciUnresolvedFix is the remediation for a golangci-lint that cannot
// analyze this module.
const golangciUnresolvedFix = "run `forge tools install golangci-lint`, or install a golangci-lint that can analyze this module " +
	"and put it first on PATH (the message names the binary, the Go versions and the command)"

// ruleGolangciUnavailable tags the gating golangci-lint lane when no
// golangci-lint that can analyze the module could be found or provisioned.
const ruleGolangciUnavailable = "golangci-lint-unavailable"
