// Rename detection for Tier-1 Go emitters.
//
// FRICTION 2026-06-02 (cp-forge dogfood pass): when `forge generate`
// renames a public symbol in a Tier-1 Go file (e.g. `db/embed.go`
// flipping `var Migrations` to `var MigrationsFS`), hand-written
// callers elsewhere in the project keep referencing the old name and
// the build breaks two runs later — long after the codegen step that
// did the rename has scrolled out of the user's terminal.
//
// Resolution: a two-step pre/post pass around the codegen body of the
// pipeline. Pre-codegen we snapshot the public exports of every
// tracked Tier-1 Go file; post-codegen we re-extract exports from the
// freshly written files and diff. Names that disappear are candidates
// for stale references — we grep the project (skipping generated
// files and the renamed file itself) for `pkg.Name` and `Name`
// patterns and surface each call site as a warning.
//
// The warnings are advisory by default. Future work: --strict-renames
// converts them to a hard pipeline error.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// stepSnapshotTier1Exports captures the pre-codegen public-export set
// of every Tier-1 Go file carrying a forge:hash marker on disk. The
// snapshot lives on ctx.PriorExports and is consumed by
// stepDetectRenamedExports after the codegen passes run.
//
// Non-Go Tier-1 files (TypeScript hooks, KCL deploy manifests, CI
// YAML) are skipped — rename detection is currently scoped to Go.
//
// Manifest enumeration goes through checksums.Inspector so the
// "Tier-1, forge-owned, Go-file" classification lives in one place
// shared with the dangling-ref check.
func stepSnapshotTier1Exports(ctx *pipelineContext) error {
	if ctx.Checksums == nil {
		return nil
	}
	insp := checksums.NewInspector(ctx.AbsPath, ctx.Checksums)
	snap := make(map[string]tier1Exports)
	for _, relPath := range insp.Tier1GoFiles() {
		content, err := os.ReadFile(filepath.Join(ctx.AbsPath, relPath))
		if err != nil {
			// File gone or unreadable — nothing to snapshot.
			continue
		}
		names, pkg := checksums.ExtractGoExports(content)
		if pkg == "" {
			continue
		}
		snap[relPath] = tier1Exports{PkgName: pkg, Names: names}
	}
	ctx.PriorExports = snap
	return nil
}

// stepDetectRenamedExports runs after codegen has written its files.
// For each Tier-1 Go file we previously snapshotted, re-extract
// exports from the current on-disk content. Names dropped between
// pre- and post- pass are reported with their stale call sites (if
// any) — the user gets a single batched warning instead of discovering
// each broken caller on the next compile.
func stepDetectRenamedExports(ctx *pipelineContext) error {
	if len(ctx.PriorExports) == 0 {
		return nil
	}
	type renameFinding struct {
		FilePath  string
		PkgName   string
		Dropped   []string
		CallSites []renameCallSite
		// MovedTo is populated when the dropped symbol now lives in a
		// different package (a cross-package move, not a pure delete).
		// MovedTo[symbol] -> list of (pkg, path) locations currently
		// declaring symbol elsewhere. Multiple entries mean the symbol
		// is now declared in more than one package — a name collision
		// the user should disambiguate before relying on the warning.
		MovedTo map[string][]checksums.SymbolLocation
	}

	// Snapshot the current project-wide exports map ONCE per generate
	// run. The map is used to detect cross-package moves: if symbol X
	// is dropped from file F (package P), but X is still declared
	// somewhere else, the warning should name BOTH the old and new
	// locations so the user can update their stale `P.X` callers.
	currentExports := checksums.ScanProjectGoExports(ctx.AbsPath)

	var findings []renameFinding
	for relPath, prior := range ctx.PriorExports {
		content, err := os.ReadFile(filepath.Join(ctx.AbsPath, relPath))
		if err != nil {
			continue
		}
		newNames, _ := checksums.ExtractGoExports(content)
		dropped := diffStringSets(prior.Names, newNames)
		if len(dropped) == 0 {
			continue
		}

		// For each dropped symbol, classify it as either (a) gone, or
		// (b) moved to one-or-more other packages. The packages we
		// search for stale references differ per case: a "gone" symbol
		// only needs the old-package grep; a "moved" symbol also needs
		// the new-package grep (callers may already be referencing the
		// new location, but old `oldPkg.X` references are still stale).
		movedTo := make(map[string][]checksums.SymbolLocation)
		searchPkgs := map[string]bool{prior.PkgName: true}
		for _, sym := range dropped {
			locs := currentExports[sym]
			// Filter out the source file itself — a same-name decl in
			// the renamed file's NEW state was already captured by the
			// newNames diff above.
			var external []checksums.SymbolLocation
			for _, loc := range locs {
				if loc.RelPath == relPath {
					continue
				}
				external = append(external, loc)
			}
			if len(external) == 0 {
				continue
			}
			movedTo[sym] = external
			for _, loc := range external {
				searchPkgs[loc.Pkg] = true
			}
		}

		// Walk every package the symbol could be referenced under and
		// merge the stale-reference findings.
		var sites []renameCallSite
		for pkg := range searchPkgs {
			sites = append(sites, findStaleReferences(ctx.AbsPath, pkg, dropped, relPath)...)
		}
		if len(sites) == 0 && len(movedTo) == 0 {
			continue
		}
		findings = append(findings, renameFinding{
			FilePath:  relPath,
			PkgName:   prior.PkgName,
			Dropped:   dropped,
			CallSites: sites,
			MovedTo:   movedTo,
		})
	}
	if len(findings) == 0 {
		return nil
	}
	// Single batched warning so the user gets one report to react to.
	fmt.Fprintf(os.Stderr, "\n⚠️  Tier-1 rename detection: %d generated file(s) dropped a public symbol with surviving caller(s):\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  • %s (package %s)\n", f.FilePath, f.PkgName)
		fmt.Fprintf(os.Stderr, "    dropped: %s\n", strings.Join(f.Dropped, ", "))
		// Surface cross-package moves so the user knows the symbol
		// isn't fully gone — it just lives somewhere else now.
		for sym, locs := range f.MovedTo {
			switch {
			case len(locs) == 1:
				fmt.Fprintf(os.Stderr, "    moved: %s now in package %s (%s)\n",
					sym, locs[0].Pkg, locs[0].RelPath)
			case len(locs) > 1:
				// Collision: more than one package declares the same
				// name. Surface every location so the user picks the
				// right destination.
				fmt.Fprintf(os.Stderr, "    moved: %s now declared in %d packages — disambiguate:\n", sym, len(locs))
				for _, loc := range locs {
					fmt.Fprintf(os.Stderr, "      - %s (%s)\n", loc.Pkg, loc.RelPath)
				}
			}
		}
		for _, s := range f.CallSites {
			fmt.Fprintf(os.Stderr, "      %s:%d: refers to %s\n", s.File, s.Line, s.Symbol)
		}
	}
	fmt.Fprintf(os.Stderr, "  Update the callers above to use the new name (or package), or `git checkout` the generated file if the rename was unintended.\n")
	return nil
}

// renameCallSite identifies one stale reference: file, line, and the
// symbol name the line still references.
type renameCallSite struct {
	File   string
	Line   int
	Symbol string
}

// diffStringSets returns the elements in `before` but not in `after`.
// Pre-sorted inputs are not required — the result is sorted for
// deterministic warning output.
func diffStringSets(before, after []string) []string {
	in := map[string]bool{}
	for _, n := range after {
		in[n] = true
	}
	var dropped []string
	for _, n := range before {
		if !in[n] {
			dropped = append(dropped, n)
		}
	}
	return dropped
}

// findStaleReferences scans Go files under root for `pkgName.symbol`
// references for each symbol in droppedNames. Skips the file being
// renamed (skipPath), generated directories (gen/, .forge/, vendor/,
// node_modules/), and files whose path indicates they are themselves
// codegen artifacts (suffix _gen.go).
//
// The scan is a regex-free textual grep — Go-aware analysis is
// overkill for advisory warnings and adds heavy dependencies. False
// positives (e.g. a comment mentioning the symbol) are acceptable
// since the warning is advisory.
func findStaleReferences(root, pkgName string, droppedNames []string, skipPath string) []renameCallSite {
	var sites []renameCallSite
	// Build the search needles once: `pkgName.Name` for each dropped
	// identifier. We prefer the qualified form to keep false-positive
	// rate down — bare `Name` matches are extremely noisy on common
	// generic names like `Run` or `New`.
	needles := make([]string, 0, len(droppedNames))
	for _, n := range droppedNames {
		needles = append(needles, pkgName+"."+n)
	}
	if len(needles) == 0 {
		return nil
	}
	skipDirs := map[string]bool{
		".git":         true,
		".forge":       true,
		"gen":          true,
		"vendor":       true,
		"node_modules": true,
		"dist":         true,
		"build":        true,
	}
	skipPathAbs := filepath.Join(root, skipPath)
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip the renamed file itself (its on-disk content may have
		// transient stale references mid-render).
		if path == skipPathAbs {
			return nil
		}
		// Skip generator output: anything ending in _gen.go or
		// _gen_test.go is owned by forge and will be rewritten.
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_gen.go") || strings.HasSuffix(base, "_gen_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(data)
		for _, needle := range needles {
			idx := 0
			for {
				found := strings.Index(text[idx:], needle)
				if found < 0 {
					break
				}
				absIdx := idx + found
				// Reject matches that are a substring of a longer identifier
				// (e.g. `pkg.Migrations` shouldn't match `pkg.MigrationsFS`).
				if absIdx+len(needle) < len(text) {
					next := text[absIdx+len(needle)]
					if isIdentChar(next) {
						idx = absIdx + len(needle)
						continue
					}
				}
				line := strings.Count(text[:absIdx], "\n") + 1
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				sites = append(sites, renameCallSite{
					File:   filepath.ToSlash(rel), // project-relative identity, as the warning prints beside marker paths
					Line:   line,
					Symbol: needle,
				})
				idx = absIdx + len(needle)
			}
		}
		return nil
	})
	return sites
}

// isIdentChar reports whether b can extend a Go identifier — used by
// the stale-reference scanner to avoid matching `Migrations` against
// `MigrationsFS`.
func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}
