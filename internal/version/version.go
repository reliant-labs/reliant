// Copyright (c) 2025 Reliant Labs

// forge:exclude-contract
//
// The four vars below are the release version stamp, written at link time by
// `-X github.com/reliant-labs/reliant/internal/version.<Name>=...` (see the
// Makefile's LDFLAGS and .github/workflows/release.yml). `-ldflags -X` can only
// write a package-level string var: converting these to getters, struct fields
// or consts makes the linker flag silently do nothing, and the binary ships
// "unknown" with no build error to catch it. They must stay package vars.
//
// Callers should read them through Get() / String(), which is the accessor the
// exported-vars rule is asking for; the vars themselves are the injection site.
package version

import (
	"runtime/debug"
	"strings"
)

// Build-time parameters set via -ldflags
var (
	Version = "unknown"
	Commit  = "unknown"
	Date    = "unknown"
	Branch  = "unknown"
)

// A user may install pug using `go install github.com/reliant-labs/reliant@latest`.
// without -ldflags, in which case the version above is unset. As a workaround
// we use the embedded build version that *is* set when using `go install` (and
// is only set for `go install` and not for `go build`).
func init() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		// < go v1.18
		return
	}
	mainVersion := info.Main.Version
	if mainVersion == "" || mainVersion == "(devel)" {
		// bin not built using `go install`
		return
	}
	// bin built using `go install`
	Version = mainVersion
}

// forgeModulePath is the CLI module. Reliant also requires
// github.com/reliant-labs/forge/pkg at the same version (a split between the
// two is caught by TestForgeModulePinsMatch in internal/buildmode), so either
// entry answers "which forge is this binary carrying".
const forgeModulePath = "github.com/reliant-labs/forge"

// Forge reports the forge version this binary was built against, read from the
// module graph the linker already embedded.
//
// Deliberately NOT another -ldflags -X var. The go.mod pin is the fact, and a
// second hand-maintained copy of it can disagree with the module actually
// linked in — which is the exact skew control-plane wants to resolve by
// deriving forge's version from reliant's pin rather than restating it.
//
// Returns "unknown" when there is no build info (e.g. a test binary that links
// no forge package).
func Forge() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		// Prefer the CLI module, but accept /pkg: which of the two appears
		// depends on what the binary imports, and both are tagged together.
		if dep.Path == forgeModulePath || strings.HasPrefix(dep.Path, forgeModulePath+"/") {
			// A replaced module reports the replacement's version; the
			// effective one is what shipped.
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			if dep.Version != "" {
				return dep.Version
			}
		}
	}
	return "unknown"
}

// BuildInfo contains all build metadata
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
	Branch  string
	// Forge is resolved from the embedded module graph, not from -ldflags.
	Forge string
}

// Get returns the current build information
func Get() BuildInfo {
	return BuildInfo{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		Branch:  Branch,
		Forge:   Forge(),
	}
}

// String returns a formatted string with all build info
func String() string {
	return Version + " (" + Commit + ", " + Date + ", " + Branch + ")"
}

// TemporalBuildID returns a stable build ID for Temporal workers.
// This prevents the Temporal SDK from computing a checksum of the binary,
// which can fail during hot reload when the binary is being rebuilt.
// In production (when Version is set via ldflags), uses the version.
// In development, uses "dev" to avoid the binary checksum computation.
func TemporalBuildID() string {
	if Version != "unknown" && Version != "" {
		// Production: use actual version for deterministic replay
		return Version
	}
	// Development: use stable "dev" identifier
	// This avoids the binary checksum race condition during Air hot reload
	return "dev"
}
