package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// forgeVersionMismatchWarning returns a warning string when the project's
// pinned forge_version (yamlVersion) doesn't match the running binary's
// version (binaryVersion), or empty if they agree / can't be compared.
//
// Three cases:
//
//  1. Project is missing forge_version entirely (legacy / pre-baseline) —
//     emit the "no forge_version declared" nudge so the user knows to run
//     `forge project upgrade` to set a baseline.
//  2. Project has a forge_version that doesn't equal the binary version —
//     emit the migration warning.
//  3. Either side is "dev" / unset / empty / a Go pseudoversion in a way
//     we can't compare — stay silent. We don't want to spam during local
//     development against a tip-of-tree forge build.
func forgeVersionMismatchWarning(yamlVersion, binaryVersion string) string {
	yamlVersion = strings.TrimSpace(yamlVersion)
	binaryVersion = strings.TrimSpace(binaryVersion)

	// Case 3: silence when the binary version isn't a real release.
	// During local forge development the binary reports "dev" / "(devel)";
	// `go install` of an un-tagged commit produces a Go pseudoversion
	// (`v0.0.0-<timestamp>-<commit>`); both are noise to dogfood owners.
	if isUnreleasedBinaryVersion(binaryVersion) {
		return ""
	}

	// Case 1: legacy project, no forge_version pinned. Treat as "0.0.0"
	// per EffectiveForgeVersion semantics.
	if yamlVersion == "" {
		return fmt.Sprintf("⚠️  no forge_version declared in forge.yaml — run '%s project upgrade' to set baseline (binary is %s).", Name(), binaryVersion)
	}

	if yamlVersion == binaryVersion {
		return ""
	}

	// Says the whole thing once. generate NOT re-pinning is the part
	// users most need: a generate that wrote the running binary's
	// version into forge.yaml is how a `+dirty` local build nobody else
	// can fetch ended up committed as a project's pin.
	return fmt.Sprintf("⚠️  forge.yaml pins forge_version %s but this binary is %s. "+
		"Generating with it anyway — generate does not re-pin the project. "+
		"To move the pin deliberately: '%s project upgrade'.",
		yamlVersion, binaryVersion, Name())
}

// isUnreleasedBinaryVersion reports whether the binary's reported version
// string corresponds to a non-release build that should suppress the
// version-pin warning. Covers:
//   - empty / unknown
//   - "dev" (local make-build sentinel)
//   - "(devel)" (Go's runtime.BuildInfo placeholder for go-run / go-test)
//   - any Go pseudoversion, in either form.
//
// Both pseudoversion forms matter. `v0.0.0-<ts>-<commit>` is what an
// un-tagged commit produces, and it was the only one recognised here.
// But once a repository HAS tags, a build from a commit after the latest
// one stamps `v0.1.25-0.<ts>-<commit>` — the base-version form — and that
// fell through to the warning. So every contributor running `task
// install:dev` in a tagged repo got told to `forge project upgrade`
// against a version no module proxy can serve, which is the exact
// "noise to dogfood owners" this function exists to prevent.
func isUnreleasedBinaryVersion(v string) bool {
	switch v {
	case "", "dev", "(devel)":
		return true
	}
	// v0.0.0- is kept as a defensive prefix match independent of the
	// suffix shape: nothing is ever released at v0.0.0, so anything
	// wearing it is a synthesised version whatever follows.
	return strings.HasPrefix(v, "v0.0.0-") || isPseudoVersion(v)
}

// pseudoVersionRE matches Go's pseudo-version suffix: a 14-digit UTC
// timestamp and a 12-hex-digit commit prefix, optionally preceded by the
// `0.` / `pre.0.` / `<n>.` counter Go inserts when the base version comes
// from a real tag. Anchoring on the SUFFIX rather than the `v0.0.0-`
// prefix is what makes both forms match — `v0.0.0-<ts>-<commit>` and
// `vX.Y.Z-0.<ts>-<commit>` alike — without mistaking an ordinary
// pre-release tag such as `v1.2.3-rc1` for one.
//
// A trailing `+dirty` (forge's own marker for a build with uncommitted
// changes) is tolerated: it makes the version LESS fetchable, not more.
var pseudoVersionRE = regexp.MustCompile(`-(?:[\w.]+\.)?[0-9]{14}-[0-9a-f]{12}(?:\+.*)?$`)

// isPseudoVersion reports whether v is a Go pseudo-version: a version
// synthesised for a commit that no tag names, which therefore no module
// proxy can serve.
func isPseudoVersion(v string) bool {
	return pseudoVersionRE.MatchString(v)
}

// versionWarnSentinelPath returns the per-binary-path sentinel file used
// to track whether the version-pin warning has already fired this shell
// session. Hashed so multiple forge binaries on the same machine (a tagged
// release + a worktree dev build) don't share state.
//
// Note: a "session" here is approximated by `$TMPDIR` — on macOS each
// shell login gets its own tmp dir, and on Linux $TMPDIR usually defaults
// to /tmp (process-shared). The sentinel-per-binary-path hash keeps the
// warning local to one forge install; the trade-off (warning fires once
// per host /tmp lifetime rather than literally once per shell) is
// acceptable for a low-volume CLI nudge.
func versionWarnSentinelPath(binaryPath string) string {
	sum := sha256.Sum256([]byte(binaryPath))
	return filepath.Join(os.TempDir(), "forge-version-warned-"+hex.EncodeToString(sum[:8]))
}

// shouldEmitVersionWarn returns whether the version-pin warning should
// be printed for this invocation, given the resolved warning string
// and the path to the running forge binary.
//
// The sentinel is honored ONLY for non-silenced binaries (Case 1/Case 2
// above). When isUnreleasedBinaryVersion returns true the caller already
// gets an empty string and shouldn't reach this gate.
func shouldEmitVersionWarn(warning, binaryPath string) bool {
	if warning == "" {
		return false
	}
	sentinel := versionWarnSentinelPath(binaryPath)
	if _, err := os.Stat(sentinel); err == nil {
		// Sentinel exists → we've already warned this session.
		return false
	}
	// Best-effort touch. If the create fails (read-only TMPDIR, etc.)
	// fall through and emit the warning; we'd rather over-warn than
	// silently swallow a real migration nudge.
	if f, err := os.Create(sentinel); err == nil {
		_ = f.Close()
	}
	return true
}
