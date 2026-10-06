// Copyright (c) 2025 Reliant Labs

// The four vars below are the release version stamp, written at link time by
// `-X github.com/reliant-labs/reliant/internal/version.<Name>=...` (see the
// Makefile's LDFLAGS and .github/workflows/release.yml). `-ldflags -X` can only
// write a package-level string var: converting these to getters, struct fields
// or consts makes the linker flag silently do nothing, and the binary ships
// "unknown" with no build error to catch it. They must stay package vars.
//
// Callers should read them through Get() / String(), which is the accessor the
// exported-vars rule is asking for; the vars themselves are the injection site.
//
//forge:exclude-contract: build-info constants stamped at link time
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

// Dirty values. "unknown" means the build carried no VCS stamp: release builds
// pass -buildvcs=false and stamp the version through -ldflags instead.
const (
	DirtyTrue    = "true"
	DirtyFalse   = "false"
	DirtyUnknown = "unknown"
)

// dirty is whether the source tree had uncommitted changes at build time, read
// from the toolchain's VCS stamp. Not an -ldflags injection site.
var dirty = DirtyUnknown

// A binary built with `go install` or a plain `go build` carries no -ldflags,
// so the vars above stay "unknown". What it DOES carry is the toolchain's own
// stamp: the module version (`go install`) and, inside a checkout, the VCS
// revision and whether the tree was modified. Without reading the latter, a
// locally installed binary reported `commit: unknown` and could not say which
// source it was built from — so two installs of "the same" reliant could not
// be told apart (dogfood finding: an installed binary days behind the code it
// was compared against, with nothing to show it).
func init() {
	if info, ok := debug.ReadBuildInfo(); ok {
		applyBuildInfo(info)
	}
}

// applyBuildInfo fills whatever -ldflags left unset from the toolchain's
// stamp. An -ldflags value always wins: a release build's stamp is deliberate,
// and the module graph of the builder's checkout is not.
func applyBuildInfo(info *debug.BuildInfo) {
	if mainVersion := info.Main.Version; Version == "unknown" && mainVersion != "" && mainVersion != "(devel)" {
		Version = mainVersion
	}
	settings := make(map[string]string, len(info.Settings))
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if rev := settings["vcs.revision"]; Commit == "unknown" && rev != "" {
		Commit = shortRevision(rev)
	}
	switch settings["vcs.modified"] {
	case "true":
		dirty = DirtyTrue
	case "false":
		dirty = DirtyFalse
	}
}

// shortRevision abbreviates a commit hash to the 12 characters Go uses in a
// pseudo-version, so the commit line matches the version line beside it.
func shortRevision(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
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
	// Dirty is DirtyTrue/DirtyFalse when the toolchain stamped VCS state,
	// DirtyUnknown otherwise.
	Dirty string
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
		Dirty:   dirty,
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
