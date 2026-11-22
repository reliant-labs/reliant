// File: internal/config/exclude.go
//
// MatchExclude is the single source of truth for `contracts.exclude`
// matching across forge. Previously the same three-rule matcher was
// hand-copied into:
//
//   - internal/config/config.go      (ContractsConfig.IsExcluded)
//   - internal/linter/contract/exclude.go (the `go vet`-driven shim)
//   - internal/contractcheck/internal_pkg_contract.go (inline closure;
//     was internal/linter/forgeconv/internal_pkg_contract.go pre-2026-06-04)
//
// All three implemented the SAME high-level rule (then: equality |
// "/"-suffix | substring; since the cp-forge authutil incident the
// substring leg is segment-boundary-aware — see MatchExclude's doc)
// but had silently diverged in two places:
//
//  1. Empty-pattern handling. config.go did NOT skip "" entries, so a
//     stray empty line in forge.yaml's contracts.exclude: would make
//     strings.Contains("", "") fire and silently exclude every
//     package. The other two filtered "" out before matching. The
//     shared helper here adopts the SKIP-EMPTY behaviour — matching
//     everything on an empty entry is never what a user wants, and the
//     YAML loader trims values but doesn't drop empties.
//
//  2. Slash normalisation. forgeconv called filepath.ToSlash(pat) before
//     comparing; the other two used the pattern verbatim. On Linux/macOS
//     this is a no-op; on Windows the pattern from forge.yaml is already
//     in slash form (it's a YAML literal) while the pkgPath the analyzer
//     produces was filepath.Join'd. The shared helper normalises BOTH
//     sides to forward slashes before matching, eliminating the
//     Windows-only divergence without changing behaviour on POSIX.
//
// In code-comment terms, the helper is the MOST-DEFENSIVE variant: it
// drops the latent empty-pattern bug from config.go and adopts
// forgeconv's portability fix.

package config

import (
	"path/filepath"
	"strings"
)

// MatchExclude reports whether pkgPath matches any of the configured
// exclude patterns. A pattern matches only on whole path-segment
// boundaries — the rule for each pattern is the union of:
//
//   - equality      (pattern == pkgPath)
//   - "/"-suffix    (pkgPath ends with "/"+pattern — the `mypkg`
//     shorthand for `internal/mypkg`, and deeper suffixes like
//     `linter/contract`)
//   - "/"-prefix    (pkgPath starts with pattern+"/" — excluding a
//     directory excludes its whole subtree)
//   - mid-path      (pkgPath contains "/"+pattern+"/" — the pattern
//     names interior segment(s) of a longer path)
//
// Empty patterns are skipped — see the package doc for why. Both
// pkgPath and patterns are normalised to forward-slash form before
// comparison (and a trailing "/" on a pattern is tolerated) so the
// helper behaves the same on every OS and on sloppy YAML input.
//
// History: this used to be a raw strings.Contains substring rule. That
// was lenient on purpose (it caught the `mypkg` shorthand), but it also
// matched PARTIAL segments — the cp-forge project excluded
// `internal/auth` and silently lost codegen for its SIBLING package
// `internal/authutil`, because "internal/authutil" contains
// "internal/auth" as a substring. Crucially there was no fuller
// spelling that could escape the over-match: the pattern already was
// the full path of the package the owner wanted excluded. The
// segment-boundary rules above keep every legitimate match the
// substring rule supported (shorthand leaf, subtree, interior
// segments) while making "exclude X" stop meaning "exclude anything
// whose name merely starts with X".
func MatchExclude(patterns []string, pkgPath string) bool {
	pkgPath = filepath.ToSlash(pkgPath)
	for _, pattern := range patterns {
		// Tolerate a trailing "/" — users write `internal/auth/` in YAML
		// to mean the directory, and pre-segment-boundary versions of
		// this matcher happened to accept it via the substring rule.
		pattern = strings.TrimSuffix(filepath.ToSlash(pattern), "/")
		if pattern == "" {
			continue
		}
		if pattern == pkgPath ||
			strings.HasSuffix(pkgPath, "/"+pattern) ||
			strings.HasPrefix(pkgPath, pattern+"/") ||
			strings.Contains(pkgPath, "/"+pattern+"/") {
			return true
		}
	}
	return false
}
