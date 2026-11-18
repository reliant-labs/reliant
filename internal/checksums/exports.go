// Public-identifier extraction for generated Go files.
//
// The rename-detection pass (see RenameWarnings in this package) needs
// to know which exported names a Tier-1 Go file declared in its most
// recent render, so it can diff against the current render and flag
// dropped names whose callers may not have been updated. This file
// owns the AST parse that produces that list.
//
// Non-Go files (.tsx, .yaml, .k, …) return an empty exports list —
// rename detection is currently scoped to Go callers because Go is the
// language where forge's codegen emits public package-level symbols
// that other (hand-written) packages depend on by name. Extending to
// TypeScript or KCL is straightforward but not needed by the current
// FRICTION class.
package checksums

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ExtractGoExports returns the sorted list of public top-level
// identifier names declared in the Go source `content`. Public is
// determined by the Go convention (first rune is uppercase).
// Functions, types, vars, and consts are all included; receiver
// methods are NOT (a method rename rarely orphans an external caller
// because it goes through an interface or value receiver).
//
// Returns (nil, "") for non-Go content (parse error) — callers should
// treat that as "no exports recorded" rather than as an error.
//
// The returned package name is the `package <name>` clause from the
// file, used by RenameWarnings to construct `pkg.Name` search
// patterns when grepping for stale callers.
func ExtractGoExports(content []byte) (exports []string, pkgName string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, parser.SkipObjectResolution)
	if err != nil {
		return nil, ""
	}
	pkgName = f.Name.Name
	seen := map[string]bool{}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			// Skip methods (Recv != nil) — see file doc.
			if d.Recv != nil {
				continue
			}
			if isExported(d.Name.Name) {
				seen[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if isExported(s.Name.Name) {
						seen[s.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if isExported(n.Name) {
							seen[n.Name] = true
						}
					}
				}
			}
		}
	}
	exports = make([]string, 0, len(seen))
	for n := range seen {
		exports = append(exports, n)
	}
	sort.Strings(exports)
	return exports, pkgName
}

// isExported reports whether the identifier begins with an uppercase
// rune (Go's convention for package-level visibility). Replicated
// here instead of importing go/token's IsExported to keep this file's
// dependency surface tight.
func isExported(name string) bool {
	if name == "" {
		return false
	}
	r := name[0]
	return r >= 'A' && r <= 'Z'
}

// IsGoPath reports whether relPath is a Go source file that the
// exports extractor should attempt to parse. Used so the
// rename-detection wiring can short-circuit on non-Go Tier-1 files.
func IsGoPath(relPath string) bool {
	return strings.HasSuffix(relPath, ".go")
}

// SymbolLocation pairs a public symbol with the package and relative
// file path that currently declares it. Used by ScanProjectGoExports
// when rename detection needs to follow a symbol that moved to a new
// package — `MigrationsFS` migrating from `db/embed.go` to
// `pkg/embed/embed.go` between forge versions, for example.
//
// The Pkg field is the declared `package <name>` clause, not the
// import path; rename detection's stale-ref grep is shape-matched
// against `pkgName.Name`, not the fully qualified import.
type SymbolLocation struct {
	Pkg     string // declared package name (e.g. "forgedb")
	RelPath string // project-relative path (e.g. "pkg/embed/embed.go")
}

// ScanProjectGoExports walks projectRoot and returns a map of public
// symbol name → every location declaring it. The same symbol declared
// in multiple packages produces a slice with multiple entries (the
// caller surfaces this as a collision warning).
//
// Skipped directories mirror the rename-detection scanner's skip
// list — generated code, vendored modules, etc., have their own
// stale-ref handling and shouldn't influence the "which packages
// declare this symbol now" answer.
//
// Returns an empty (non-nil) map on filepath.Walk error so callers can
// always range over the result without nil-checks.
func ScanProjectGoExports(projectRoot string) map[string][]SymbolLocation {
	out := make(map[string][]SymbolLocation)
	skipDirs := map[string]bool{
		".git":         true,
		".forge":       true,
		"gen":          true,
		"vendor":       true,
		"node_modules": true,
		"dist":         true,
		"build":        true,
		"testdata":     true,
	}
	_ = filepath.Walk(projectRoot, func(path string, info os.FileInfo, err error) error {
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
		if strings.HasSuffix(path, "_test.go") {
			// Tests don't expose API surface external callers depend on.
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		names, pkg := ExtractGoExports(content)
		if pkg == "" {
			return nil
		}
		rel, relErr := filepath.Rel(projectRoot, path)
		if relErr != nil {
			rel = path
		}
		// Slash-separated, like every other project-relative identity forge
		// reports (see slashKey): this lands in user-facing warnings beside
		// marker paths, and must not read cmd\x.go on Windows next to them.
		loc := SymbolLocation{Pkg: pkg, RelPath: filepath.ToSlash(rel)}
		for _, n := range names {
			out[n] = append(out[n], loc)
		}
		return nil
	})
	return out
}
