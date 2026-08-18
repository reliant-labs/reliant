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

import "runtime/debug"

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

// BuildInfo contains all build metadata
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
	Branch  string
}

// Get returns the current build information
func Get() BuildInfo {
	return BuildInfo{
		Version: Version,
		Commit:  Commit,
		Date:    Date,
		Branch:  Branch,
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
