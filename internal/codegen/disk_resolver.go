package codegen

// disk_resolver.go — disk-first resolution of Go package identity for
// EXISTING components (services, workers, operators, internal packages).
//
// Background (kalshi-trader FORGE_BACKLOG #1, 2026-06): bootstrap.go and
// wire_gen.go used to RE-SYNTHESIZE the on-disk directory + Go package
// name from the forge.yaml / proto service name on every regenerate
// (toServicePackage / toGoPackage → compact lowercase, separators
// stripped). Any project whose directories don't match that exact
// synthesis — e.g. workers/engine_shadow scaffolded under the
// pre-2026-06-08 snake_case era, or hand-renamed dirs — got import lines
// pointing at directories that don't exist (workers/engineshadow) and
// EMPTY Deps literals (the Deps AST probe looked in the synthesized dir,
// found nothing, and silently wired nothing). Users were forced to
// `git checkout pkg/app/wire_gen.go pkg/app/bootstrap.go` after every
// `forge generate`.
//
// The fix is to treat the filesystem as the source of truth for any
// component that already exists:
//
//   - ResolveComponentDir locates the component's directory under its
//     role root (handlers/, workers/, operators/, internal/) by probing
//     the naming variants forge has ever emitted (literal, snake_case,
//     compact, kebab-case), then
//   - ParsePackageClause reads the REAL `package x` clause from the
//     directory's .go files (go/parser, PackageClauseOnly — cheap).
//
// Name synthesis (toServicePackage / toGoPackage) remains ONLY the
// fallback for components that don't exist on disk yet — i.e. brand-new
// scaffolds, where forge is about to create the directory and therefore
// gets to pick the name.
//
// Mismatch diagnostics: when the directory exists but its package
// identity can't be determined (no parseable .go file, or files that
// disagree on the package clause), resolution FAILS with an actionable
// error naming the offending files — silently falling back to synthesis
// here would reintroduce the exact bug class this file eliminates.

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// ResolvedComponent is the disk-first identity of one component.
type ResolvedComponent struct {
	// Dir is the component's source directory (projectDir/roleRoot/ImportLeaf).
	// Always populated — when FromDisk is false it points at the directory
	// forge WOULD create for a fresh scaffold of this name.
	Dir string

	// ImportLeaf is the directory path relative to the role root, in
	// forward-slash form (e.g. "engine_shadow", "mcp/database"). This is
	// the segment generated import lines must use — it reflects what is
	// actually on disk, not what the naming rules would synthesize.
	ImportLeaf string

	// PackageName is the Go package name. When FromDisk is true it is the
	// package clause parsed from the directory's .go files (which may
	// legally differ from the directory name, e.g. workers/engine_shadow
	// declaring `package engineshadow`). When FromDisk is false it is the
	// synthesized scaffold name.
	PackageName string

	// FromDisk reports whether the directory was found on disk. False
	// means the caller is looking at a component that hasn't been
	// scaffolded yet and PackageName/ImportLeaf are synthesized.
	FromDisk bool
}

// componentDirCandidates returns the on-disk directory names to probe for
// a component called name, in priority order. The list covers every
// shape forge has ever emitted plus the user-facing spellings:
//
//  1. the literal name (forge.yaml spelling, possibly nested "a/b")
//  2. its lowercase form
//  3. snake_case  ("EngineShadow" / "engine-shadow" → "engine_shadow")
//  4. compact     ("engine_shadow" → "engineshadow"; the pre-2026-06-08
//     compact-interlude scaffold shape)
//  5. kebab-case  ("EngineShadow" → "engine-shadow")
//
// Duplicates are removed preserving order. Probing stops at the first
// existing directory, so a project that (thanks to the historical
// duplicate-dir bug) contains BOTH workers/engine_shadow and
// workers/engineshadow deterministically resolves to the more specific
// snake form.
func componentDirCandidates(name string) []string {
	base := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(name)), "/")
	candidates := []string{
		base,
		strings.ToLower(base),
		strings.ReplaceAll(naming.ToSnakeCase(base), "-", "_"),
		compactPackageForm(base),
		naming.ToKebabCase(base),
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// ResolveComponentDir locates the existing directory for a component
// (role root "handlers", "workers", "operators", or "internal") and
// returns its disk-first identity: the real directory leaf for import
// lines plus the real package clause for selectors/aliases.
//
// When no candidate directory exists, the synthesized scaffold identity
// is returned with FromDisk=false and a nil error — that's the
// legitimate "forge is about to create this component" path. An empty
// projectDir always takes the synthesized path (callers like unit tests
// pass "" to mean "no project context").
//
// When a candidate directory exists but its package clause can't be
// determined (no parseable .go file, or conflicting clauses), the error
// from ParsePackageClause is returned verbatim — see that function for
// the diagnostic shape. Callers must NOT swallow this error and fall
// back to synthesis: emitting a guessed import/selector for a directory
// that demonstrably exists is the silent-corruption mode this resolver
// exists to kill.
func ResolveComponentDir(projectDir, roleRoot, name string) (ResolvedComponent, error) {
	// Synthesized fallback, matching the post-2026-06-08 canonical
	// scaffold rule (naming.GoPackage): snake_case collapses PascalCase,
	// kebab, and snake spellings to the same snake identifier — the form
	// the scaffolders (naming.ServicePackage / naming.GoPackage call
	// sites) actually create on disk, so scaffold + generate agree.
	synth := naming.GoPackage(strings.TrimPrefix(filepath.ToSlash(name), "/"))
	synthPkg := synth
	if idx := strings.LastIndex(synthPkg, "/"); idx >= 0 {
		synthPkg = synthPkg[idx+1:]
	}
	fallback := ResolvedComponent{
		Dir:         filepath.Join(projectDir, roleRoot, filepath.FromSlash(synth)),
		ImportLeaf:  synth,
		PackageName: synthPkg,
		FromDisk:    false,
	}
	if projectDir == "" {
		return fallback, nil
	}

	rootDir := filepath.Join(projectDir, roleRoot)
	// List the role root once so flat candidates match the EXACT on-disk
	// spelling. Probing with os.Stat alone would falsely match a
	// differently-cased directory on case-insensitive filesystems
	// (macOS) and emit an import path that breaks on case-sensitive CI.
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		// Role root missing entirely (no workers/ dir yet, etc.) — pure
		// scaffold territory.
		return fallback, nil
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			onDisk[e.Name()] = true
		}
	}

	for _, cand := range componentDirCandidates(name) {
		dir := filepath.Join(rootDir, filepath.FromSlash(cand))
		if strings.Contains(cand, "/") {
			// Nested candidate (internal packages like "mcp/database") —
			// fall back to a stat probe.
			fi, statErr := os.Stat(dir)
			if statErr != nil || !fi.IsDir() {
				continue
			}
		} else if !onDisk[cand] {
			continue
		}
		pkgName, perr := ParsePackageClause(dir)
		if perr != nil {
			return ResolvedComponent{}, fmt.Errorf("resolving %s component %q: %w", roleRoot, name, perr)
		}
		return ResolvedComponent{
			Dir:         dir,
			ImportLeaf:  cand,
			PackageName: pkgName,
			FromDisk:    true,
		}, nil
	}
	return fallback, nil
}

// compactPackageForm is the pre-2026-06-08 compact-interlude package rule:
// lowercase with hyphen/underscore separators stripped ("engine_shadow" →
// "engineshadow"). Kept ONLY as a directory-probe candidate so projects
// scaffolded during that era still resolve; new synthesis is snake_case
// via naming.GoPackage.
func compactPackageForm(name string) string {
	return compactPackageReplacer.Replace(strings.ToLower(name))
}

var compactPackageReplacer = strings.NewReplacer("-", "", "_", "")

// ResolveServiceComponent is ResolveComponentDir specialized for
// services: it strips the proto "Service" suffix (callers hold either
// the proto name "EngineShadowService" or the forge.yaml name
// "engine-shadow"; both must resolve to the same handlers/<x> dir) and
// probes under handlers/.
func ResolveServiceComponent(projectDir, svcName string) (ResolvedComponent, error) {
	trimmed := strings.TrimSuffix(svcName, "Service")
	if trimmed == "" {
		trimmed = svcName
	}
	return ResolveComponentDir(projectDir, "internal/handlers", trimmed)
}

// ParsePackageClause returns the Go package name declared by the .go
// files directly inside dir (PackageClauseOnly parse — cheap; no type
// checking, no imports). _test.go files and files starting with "." or
// "_" are skipped, matching the go tool's build-file rules; external
// test packages ("foo_test") therefore can't pollute the result.
//
// Errors are diagnostics, not soft fallbacks:
//   - dir unreadable or containing no buildable .go file → error telling
//     the user the directory can't be used as a component source dir;
//   - files disagreeing on the package clause → error listing each
//     file:line with its declared package so the user can fix the stray
//     clause directly.
func ParsePackageClause(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", dir, err)
	}

	fset := token.NewFileSet()
	// clause name → "file:line" locations declaring it (sorted for
	// deterministic error output).
	clauses := map[string][]string{}
	var parseFailures []string
	goFiles := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") ||
			strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		goFiles++
		path := filepath.Join(dir, name)
		f, perr := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if perr != nil {
			parseFailures = append(parseFailures, perr.Error())
			continue
		}
		pos := fset.Position(f.Name.Pos())
		clauses[f.Name.Name] = append(clauses[f.Name.Name], fmt.Sprintf("%s:%d", path, pos.Line))
	}

	switch {
	case len(clauses) == 1:
		for name := range clauses {
			return name, nil
		}
	case len(clauses) == 0:
		if goFiles == 0 {
			return "", fmt.Errorf("%s exists but contains no buildable .go file — forge cannot determine its Go package name; add the component's source file (or remove the stale directory) and re-run forge generate", dir)
		}
		return "", fmt.Errorf("%s contains no parseable .go file — forge cannot determine its Go package name; fix the syntax error(s) and re-run forge generate:\n  %s", dir, strings.Join(parseFailures, "\n  "))
	}

	// Multiple package clauses in one directory: list every declaration
	// site so the user can fix the stray file(s) directly.
	names := make([]string, 0, len(clauses))
	for name := range clauses {
		names = append(names, name)
	}
	sort.Strings(names)
	var detail strings.Builder
	for _, name := range names {
		locs := clauses[name]
		sort.Strings(locs)
		for _, loc := range locs {
			fmt.Fprintf(&detail, "\n  %s: package %s", loc, name)
		}
	}
	return "", fmt.Errorf("%s declares conflicting package clauses (%s) — every .go file in a component directory must declare the same package; fix the stray clause(s) and re-run forge generate:%s",
		dir, strings.Join(names, ", "), detail.String())
}
