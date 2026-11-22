// File: internal/linter/forgeconv/optional_dep_marker.go
//
// The forgeconv-optional-dep-marker-position analyzer enforces that the
// `// forge:optional-dep` marker, when present, sits in the doc-comment
// slot directly above a `Deps` struct field — never on the struct
// itself, never on a non-Deps type, and never on a free-floating
// comment unconnected to a field.
//
// Why this exists
//
// The marker has wire_gen + validateDeps semantics that only fire when
// it lands on a struct field that ParseServiceDeps recognizes. A typo
// like marking the struct (`// forge:optional-dep\ntype Deps struct`)
// or misplacing it on a method docstring would silently skip the
// optional-ness — wire_gen would still emit a TODO + UNRESOLVED entry,
// validateDeps would still gate the field, and the user would think
// the marker is working. Catch the misuse at lint time.
//
// Detection
//
// Per-file walk over `handlers/<svc>/`, `workers/<wkr>/`, and
// `operators/<op>/`:
//
//   1. Parse each non-test .go file with comments enabled.
//   2. Scan EVERY comment in the file for the `forge:optional-dep`
//      directive (using HasOptionalDepMarker — the same recognition
//      the deps_parser uses).
//   3. For every match, classify the location:
//        - Doc / inline comment of a `Deps` struct field → OK.
//        - Anywhere else → emit a finding.
//
// The rule is severity=error: misplaced markers are a programming
// mistake the user wants surfaced loudly. False positives are
// extremely rare — the directive's role is dedicated and the spelling
// is deliberately specific.

package forgeconv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// LintOptionalDepMarkerPosition walks rootDir's internal/handlers/,
// internal/workers/, and internal/operators/ trees for files whose comments mention the
// `forge:optional-dep` marker, and emits a finding when the marker is
// not attached to a `Deps` struct field. Returns findings in
// deterministic order (file, then line). Missing component dirs are
// not an error — projects may ship none of them.
func LintOptionalDepMarkerPosition(rootDir string) (Result, error) {
	var pkgDirs []string
	for _, sub := range []string{"internal/handlers", "internal/workers", "internal/operators"} {
		root := filepath.Join(rootDir, sub)
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !d.IsDir() {
				return nil
			}
			if d.Name() == "testdata" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			// Only directories that actually contain .go files. The
			// per-component subdir (handlers/<svc>) is what we want;
			// the parent (handlers/) doesn't normally hold .go files
			// but if it does we lint those too.
			entries, readErr := os.ReadDir(p)
			if readErr != nil {
				return nil
			}
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
					pkgDirs = append(pkgDirs, p)
					break
				}
			}
			return nil
		})
		if err != nil {
			return Result{}, fmt.Errorf("walk %s: %w", root, err)
		}
	}
	sort.Strings(pkgDirs)

	var result Result
	for _, dir := range pkgDirs {
		findings, err := lintOptionalDepMarkerPkg(dir, rootDir)
		if err != nil {
			return Result{}, err
		}
		result.Findings = append(result.Findings, findings...)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

// lintOptionalDepMarkerPkg parses every non-test .go file in pkgDir,
// finds every comment mentioning `forge:optional-dep`, and reports a
// finding when the comment is NOT the doc / inline-comment slot of a
// `Deps` struct field.
func lintOptionalDepMarkerPkg(pkgDir, rootDir string) ([]Finding, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pkgDir, err)
	}

	var findings []Finding
	fset := token.NewFileSet()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		fp := filepath.Join(pkgDir, e.Name())
		file, parseErr := parser.ParseFile(fset, fp, nil, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			// Don't double-report parse errors — the Go toolchain will.
			continue
		}

		// Collect every position that IS a legitimate Deps-field
		// comment slot. We use the comment-group's End position as the
		// key so both a leading doc and a trailing inline comment can
		// be looked up by their group's End.
		legitGroups := map[token.Pos]bool{}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "Deps" {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structType.Fields.List {
					if field.Doc != nil {
						legitGroups[field.Doc.End()] = true
					}
					if field.Comment != nil {
						legitGroups[field.Comment.End()] = true
					}
				}
			}
		}

		// Now scan every comment group in the file. If it carries the
		// marker AND is not in legitGroups, report it.
		rel, relErr := filepath.Rel(rootDir, fp)
		if relErr != nil {
			rel = fp
		}
		for _, cg := range file.Comments {
			// Use the AST-aware variant — cg.Text() drops the
			// `//forge:optional-dep` directive form (no space after
			// `//`), which is exactly the typo this lint exists to
			// catch. Iterating cg.List recovers both spaced and
			// unspaced forms.
			if !codegen.HasOptionalDepMarkerCommentGroup(cg) {
				continue
			}
			if legitGroups[cg.End()] {
				continue
			}
			pos := fset.Position(cg.Pos())
			findings = append(findings, Finding{
				Rule:        "forgeconv-optional-dep-marker-position",
				Severity:    SeverityError,
				File:        rel,
				Line:        pos.Line,
				Message:     "`// forge:optional-dep` marker is not attached to a `Deps` struct field — wire_gen and validateDeps will silently ignore it",
				Remediation: "place the marker on the line directly above (or as the inline comment after) a field declaration in the `Deps` struct. Example:\n        // NATSPublisher publishes domain events; nil disables rollback.\n        // forge:optional-dep\n        NATSPublisher EventPublisher",
			})
		}
	}
	return findings, nil
}
