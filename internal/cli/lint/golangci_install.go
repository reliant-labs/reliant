package lint

import (
	"context"
	"fmt"
	"go/version"
	"os/exec"
	"regexp"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/templates"
)

// The exported half of golangci_toolchain.go, for `forge tools install`: it
// installs the golangci-lint `forge lint` gates with — forge's pin, built
// with the toolchain that compiles the project — onto PATH, so a machine set
// up with `forge tools install` never pays forge lint's one-time build.

// GolangciLintPin is the golangci-lint release forge lints with and installs.
func GolangciLintPin() string { return templates.GolangciLintVersion }

// GolangciLintModule is the package `go install` builds golangci-lint from.
func GolangciLintModule() string { return golangciModulePath }

// GolangciLintOnPath reports the golangci-lint PATH resolves and whether it
// is forge's pin built with a Go that can analyze the module at dir. why says
// what is wrong with it ("" when nothing is); path is "" when there is none.
func GolangciLintOnPath(dir string) (path, why string) {
	path, err := exec.LookPath("golangci-lint")
	if err != nil {
		return "", "not on PATH"
	}
	// Build info first (no subprocess); a wrapper script or a release
	// build that records "(devel)" is asked instead.
	b, _ := readGolangciBuild(path)
	if !semver.IsValid(b.version) || b.goVersion == "" {
		v, goV := golangciReportedVersion(path)
		if !semver.IsValid(b.version) {
			b.version = v
		}
		if b.goVersion == "" {
			b.goVersion = goV
		}
	}
	if b.version != templates.GolangciLintVersion {
		return path, fmt.Sprintf("is %s, not forge's pin %s", describeVersion(b.version), templates.GolangciLintVersion)
	}
	if modLang := moduleGoDirective(dir); modLang != "" && version.IsValid(b.goVersion) && !langAtLeast(b.goVersion, modLang) {
		return path, fmt.Sprintf("was built with %s, older than this module's go %s", b.goVersion, modLang)
	}
	return path, ""
}

// golangciVersionLine is how `golangci-lint --version` states its version:
// "golangci-lint has version 2.14.0 built with go1.27.0 from …".
var golangciVersionLine = regexp.MustCompile(`has version v?(\d+\.\d+\.\d+)(?: built with (go\d+\.\d+(?:\.\d+)?))?`)

// golangciReportedVersion is what the binary at path says it is: its version
// ("v2.14.0") and the Go it was built with ("go1.27.0"), "" for either it
// does not state.
func golangciReportedVersion(path string) (ver, goVersion string) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", ""
	}
	m := golangciVersionLine.FindSubmatch(out)
	if m == nil {
		return "", ""
	}
	return "v" + string(m[1]), string(m[2])
}

func describeVersion(v string) string {
	if v == "" {
		return "an unknown version"
	}
	return v
}

// InstallGolangciLint builds forge's pinned golangci-lint with the toolchain
// `go` selects for the module at dir, into gobin, and returns that
// toolchain. The build runs outside any module, so the project's go.mod and
// go.work cannot steer it.
func InstallGolangciLint(ctx context.Context, dir, gobin string) (string, error) {
	toolchain, err := moduleToolchainVersion(ctx, dir)
	if err != nil {
		return "", err
	}
	// Staged under gobin itself so the final rename never crosses a
	// filesystem; a golangci-lint already there is replaced only by a
	// complete binary.
	tc := golangciToolchain{install: installGolangciLint, pin: templates.GolangciLintVersion}
	return toolchain, installAtomically(ctx, tc, gobin, gobin, toolchain)
}
