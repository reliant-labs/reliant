// Canonical Go formatting — the formatter half of the self-certification
// contract for .go output.
//
// ── Why the writer formats before it stamps ───────────────────────────
//
// The embedded forge:hash marker certifies exact bytes (modulo the
// line-ending/trailing-newline normalization documented in stamp.go).
// Go files, however, live under tooling that routinely rewrites bytes
// without changing meaning: gofmt realigns whitespace, goimports
// regroups import blocks (stdlib / third-party / local). If forge
// stamps a render that is NOT already in that canonical form, the first
// formatter pass over the tree turns every pristine render into byte
// drift, and the Tier-1 stomp guard reads it as a hand-edit
// (fr: control-plane 2026-07-08 — four internal/*/mock_gen.go files
// hard-blocked `forge generate` solely because a goimports pass had
// regrouped and aliased their imports).
//
// The fix has two halves:
//
//  1. Format-before-stamp: WriteGeneratedFile passes every .go render
//     through CanonicalGoSource BEFORE computing the stamp, so the
//     certified bytes are a fixed point of the canonical formatter and
//     a user's formatter pass is a no-op.
//  2. Normalize-on-compare: where a marker mismatches (the guard, the
//     writer's Modified branch), the on-disk bytes are re-hashed AFTER
//     the same canonical formatting; a match means formatter-only drift
//     — still forge's render, not a hand-edit. Non-Go formats keep
//     exact-byte semantics.
//
// ── The canonical formatter ───────────────────────────────────────────
//
// CanonicalGoSource is goimports' formatting engine
// (golang.org/x/tools/imports.Process) in FormatOnly mode: gofmt plus
// import-block merging, sorting, and group splitting — but never the
// filesystem-scanning insertion/removal of imports, which would make
// output environment-dependent. The local-import prefix is the
// project's MODULE PATH, matching the two places forge already commits
// to that convention for user projects:
//
//   - the generate pipeline's post-write pass:
//     `goimports -local <module> -w …` (runGoimportsOnGenerated), and
//   - the scaffolded .golangci.yml:
//     `formatters.settings.goimports.local-prefixes: [<module>]`.
//
// A pass of `goimports -local <module>` (or a golangci-lint fmt run
// with the scaffolded config) over canonical output is therefore a
// byte-level no-op. The one remaining semantic pass — the pipeline's
// real goimports fixing an import list — is already covered by
// RestampWritten.
package checksums

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/imports"
)

// isGoSource reports whether relPath names a Go source file — the only
// format the canonical-formatter machinery applies to.
func isGoSource(relPath string) bool {
	return strings.HasSuffix(strings.ToLower(relPath), ".go")
}

// goFormatMu serializes CanonicalGoSource: x/tools/imports carries the
// local-prefix option in a package-level variable (imports.LocalPrefix),
// so concurrent callers with different prefixes would race.
var goFormatMu sync.Mutex

// CanonicalGoSource formats Go source exactly the way the pipeline's
// `goimports -local <localPrefix>` pass formats it, minus the
// import insertion/removal (FormatOnly): gofmt plus import-block
// merge/sort/group-split with localPrefix sorted into its own trailing
// group. Deterministic and hermetic — no filesystem or module-cache
// scanning. filename is advisory (error messages); src is never read
// from disk.
//
// The result is a fixed point: CanonicalGoSource(CanonicalGoSource(x))
// == CanonicalGoSource(x).
func CanonicalGoSource(localPrefix, filename string, src []byte) ([]byte, error) {
	goFormatMu.Lock()
	defer goFormatMu.Unlock()
	prev := imports.LocalPrefix
	imports.LocalPrefix = localPrefix
	defer func() { imports.LocalPrefix = prev }()
	return imports.Process(filename, src, &imports.Options{
		Comments:   true,
		TabIndent:  true,
		TabWidth:   8,
		FormatOnly: true, // never add/remove imports — deterministic, no fs scan
	})
}

// modulePathCache memoizes GoImportsLocalPrefix per absolute project
// root. Only successful lookups are cached: `forge project new` writes go.mod
// mid-scaffold, and caching an early miss would pin the wrong prefix
// for the rest of the process.
var (
	modulePathMu    sync.Mutex
	modulePathCache = map[string]string{}
)

// GoImportsLocalPrefix returns the canonical goimports local-import
// prefix for the project at root: the module path declared in
// root/go.mod. Empty when go.mod is absent or carries no module
// directive — canonical formatting then simply has no local group,
// matching what a bare `goimports` pass would do.
func GoImportsLocalPrefix(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	modulePathMu.Lock()
	defer modulePathMu.Unlock()
	if p, ok := modulePathCache[abs]; ok {
		return p
	}
	data, rerr := os.ReadFile(filepath.Join(abs, "go.mod"))
	if rerr != nil {
		return "" // not cached — go.mod may appear later in this process
	}
	p := modfile.ModulePath(data)
	if p != "" {
		modulePathCache[abs] = p
	}
	return p
}

// canonicalizeGoForWrite runs a .go render through the canonical
// formatter before it is stamped and written. Best-effort by design:
// non-Go paths and unparseable content are returned verbatim — a
// template emitting broken Go should fail at the pipeline's
// `go build ./...` validate step with a real compiler error, not at
// the write chokepoint (and the exact-byte stamp semantics simply
// remain in force for such a file).
func canonicalizeGoForWrite(root, relPath string, content []byte) []byte {
	if !isGoSource(relPath) {
		return content
	}
	formatted, err := CanonicalGoSource(GoImportsLocalPrefix(root), relPath, content)
	if err != nil {
		return content
	}
	return formatted
}

// canonicalGoBody returns the BodyHash of content after canonical
// formatting — the normalize-on-compare primitive. ok=false when
// relPath is not Go source or the content does not parse (a hand-edit
// that broke the file is definitively not formatter noise).
func canonicalGoBody(root, relPath string, content []byte) (string, bool) {
	if !isGoSource(relPath) {
		return "", false
	}
	formatted, err := CanonicalGoSource(GoImportsLocalPrefix(root), relPath, content)
	if err != nil {
		return "", false
	}
	return BodyHash(formatted), true
}

// CanonicalBodyHash is canonicalGoBody in total form: the body hash of
// content after canonical Go formatting, falling back to the exact-byte
// BodyHash for non-Go paths and for content that does not parse. It
// gives callers one rule for "are these two files the same render?"
// regardless of format — the .go tolerance applies where it is
// meaningful and is a no-op everywhere else.
func CanonicalBodyHash(root, relPath string, content []byte) string {
	if h, ok := canonicalGoBody(root, relPath, content); ok {
		return h
	}
	return BodyHash(content)
}

// ClassifyPath is the path-aware form of Verify: the exact-byte
// classification widened by the .go formatter tolerance, so import
// regrouping or a gofmt realignment reads as Pristine rather than as a
// hand-edit. Verify itself stays exact-byte by design (it knows nothing
// about paths); every gate that has a path in hand should classify
// through here so the drift scan, the writer, and the artifact-
// retirement sweep all answer the same question the same way.
// Marker detection is path-aware too: a marker in a comment syntax
// relPath's own format cannot produce is documentation about the marker,
// not a stamp, so such a file classifies NoMarker (forge claims no
// ownership) rather than Modified. See ExtractMarkerFor.
func ClassifyPath(root, relPath string, content []byte) VerifyStatus {
	embedded, found := ExtractMarkerFor(relPath, content)
	if !found {
		return NoMarker
	}
	if embedded == BodyHash(content) {
		return Pristine
	}
	if goFormatterEquivalent(root, relPath, content, embedded) {
		return Pristine
	}
	return Modified
}

// goFormatterEquivalent reports whether content is a forge render
// certified by embedded, MODULO canonical formatting: hash the bytes
// again after CanonicalGoSource and compare. True means the drift is
// formatter noise (import regrouping, gofmt realignment) — the file is
// clean, not hand-edited. Comment/text/code edits survive formatting
// and therefore still mismatch.
func goFormatterEquivalent(root, relPath string, content []byte, embedded string) bool {
	if embedded == "" || embedded == UnverifiedMarkerValue {
		return false
	}
	body, ok := canonicalGoBody(root, relPath, content)
	return ok && body == embedded
}
