package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/webruntimepeers"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The vitest `resolve.dedupe` reconcile, for projects that predate it.
//
// WHY FORGE TOUCHES A USER-OWNED FILE AT ALL. vitest.config.ts is
// scaffold-once — written at birth and never rewritten, because a test config
// is exactly the kind of thing a project outgrows. But forge moved the
// generated hooks' React Query machinery into @reliantlabs/forge-web-runtime, and
// that move has a consequence no existing project can see coming:
//
// A dev build of forge bridges the runtime with a `file:` specifier, which npm
// materialises as a SYMLINK to the forge checkout. npm cannot hoist a linked
// package's own node_modules away, and a bundler resolves a module's bare
// imports by walking up from that module's own location. So the runtime binds
// React and @tanstack/react-query out of the FORGE CHECKOUT while the app
// binds its own. Two copies of React Query means two React contexts — and
// since every generated `use<Rpc>` now runs inside the runtime's factory, the
// <QueryClientProvider> a test mounts is invisible to the hook beneath it:
//
//	TypeError: Cannot read properties of null (reading 'useContext')
//
// The project did nothing wrong and has no way to know: it typechecks, it
// builds, and the failure is in a file forge generated, caused by a
// dependency edge forge introduced. Leaving it is not "preserving the user's
// config", it is shipping them a red test suite. So the same reasoning that
// justifies rewriteRenamedFrontendImports applies here — and this pass is
// held to the same limits: it adds ONE key to the existing resolve block,
// changes nothing else, and does nothing at all when the key is present.
//
// A registry (non-dev) install hoists one copy and never had the problem, so
// this line is inert there. It is written unconditionally anyway: the same
// project is bridged and unbridged at different times depending on who built
// the forge binary, and a config that only works under one of them is worse
// than one that works under both.

// dedupeKeyRe finds an existing `dedupe:` entry inside a resolve block — the
// signal that this config already handles the hazard (either from a current
// scaffold or from a previous run of this pass).
var dedupeKeyRe = regexp.MustCompile(`(?m)^\s*dedupe\s*:`)

// resolveOpenRe matches the opening of vitest's `resolve: {` block, which is
// where the dedupe key is inserted. Anchored on the key so it cannot match
// `path.resolve(` or a `resolve` import.
var resolveOpenRe = regexp.MustCompile(`(?m)^(\s*)resolve\s*:\s*\{`)

// dedupedPackages are the packages that must resolve to exactly one copy.
//
// Spelled out rather than read from the runtime's package.json (as the
// scaffolded config does) because this pass EDITS an existing file: a literal
// list is a one-line insertion that a user can read and delete, while
// injecting the file-reading preamble would mean restructuring imports in a
// config forge does not own. The scaffolded form stays derived; this repair
// stays minimal.
// DERIVED, not hand-written. This was a literal that had already drifted from
// the runtime's real peerDependencies by nine packages (including
// @opentelemetry/api). See internal/webruntimepeers for the source of truth.
func dedupeLine() string {
	pkgs := webruntimepeers.BundlerDedupe()
	quoted := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		quoted = append(quoted, `"`+p+`"`)
	}
	return `dedupe: [` + strings.Join(quoted, ", ") + `],`
}

// reconcileFrontendDedupe adds `resolve.dedupe` to each frontend's
// vitest.config.ts when it is missing.
//
// Best-effort and non-fatal throughout: a frontend with no config, an
// unreadable file, or a config whose resolve block this cannot find is
// skipped rather than failed. Idempotent — a config that already has the key
// is left byte-identical, which is what keeps `forge generate` twice in a row
// reporting no changes.
func reconcileFrontendDedupe(cfg *config.ProjectConfig, projectDir string) {
	if cfg == nil {
		return
	}

	var touched []string
	for _, fe := range cfg.Frontends {
		// Only the browser frontends run vitest against the runtime's hooks.
		if !isWebFrontendType(fe.Type) {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}
		rel := filepath.Join(feDir, "vitest.config.ts")
		if addDedupeToConfig(filepath.Join(projectDir, rel)) {
			touched = append(touched, filepath.ToSlash(rel))
		}
	}

	if len(touched) > 0 {
		fmt.Printf("  ♻️  added resolve.dedupe to %d vitest config(s) — one copy of React/React Query\n", len(touched))
		for _, rel := range touched {
			fmt.Printf("      - %s\n", rel)
		}
	}
}

// addDedupeToConfig inserts the dedupe key into one config file. Returns true
// when the file changed.
func addDedupeToConfig(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false // no config here — nothing to reconcile
	}
	if dedupeKeyRe.Match(body) {
		return false // already handled
	}
	loc := resolveOpenRe.FindSubmatchIndex(body)
	if loc == nil {
		// A config that has been restructured past recognition. Forge does
		// not guess at where a key belongs in a file it does not own.
		return false
	}
	// loc[2]:loc[3] is the indentation captured before `resolve:`; the new key
	// sits one level deeper, matching the block it is joining.
	indent := string(body[loc[2]:loc[3]])
	insertAt := loc[1] // just past the `{`

	var out bytes.Buffer
	out.Write(body[:insertAt])
	out.WriteString("\n" + indent + "  // Added by forge: the generated hooks call into\n")
	out.WriteString(indent + "  // @reliantlabs/forge-web-runtime, which a dev forge build links by path.\n")
	out.WriteString(indent + "  // Without this, React and React Query load twice (once from the\n")
	out.WriteString(indent + "  // link target) and the two copies do not share a React context.\n")
	out.WriteString(indent + "  " + dedupeLine())
	out.Write(body[insertAt:])

	info, err := os.Stat(path)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = info.Mode().Perm()
	}
	checksums.RecordPreWriteAbs(path) // journaled: a failed run restores it
	return atomicfile.WriteFile(path, out.Bytes(), mode) == nil
}
