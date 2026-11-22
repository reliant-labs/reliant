// Disowned-sibling dangling-reference detection.
//
// FRICTION 2026-06-04 (cp-forge layer-6 workers lane, fork era): the
// user had frozen `pkg/app/bootstrap.go` and `pkg/app/wire_gen.go` out
// of regeneration. After adding a worker, `forge generate` happily
// re-emitted `pkg/app/app_gen.go` — and the freshly regenerated
// `app_gen.go` declared `Workers *Workers`. But the `Workers` TYPE was
// defined inside the frozen `bootstrap.go`, still at its pre-workers
// content. `go build` then failed with `pkg/app/app_gen.go:42:13:
// undefined: Workers` — a silent build break far from its cause.
//
// The same hazard exists for DISOWNED files (the frozen-file mechanics
// are identical: forge never re-emits them, even with `--force`), so
// the check survives the fork removal with disowned entries as its
// subject. Resolution: after codegen finishes, scan the regenerated
// forge-owned Tier-1 files inside the same package as any disowned
// sibling. For each referenced package-local type name that is NOT
// defined in either
//
//   - the file doing the reference, or
//   - any forge-owned sibling in the same package, or
//   - any disowned sibling in the same package,
//
// surface a loud, actionable error listing the type, the call site,
// the disowned sibling that probably ought to have defined it, and the
// concrete escape hatches (hand-add the missing declaration to the
// disowned file — it's yours — or re-adopt it by deleting it and
// re-running `forge generate`).
//
// The scan is restricted to the same Go package as the disowned file
// because that's where Go's package-local resolution applies — a
// dangling unqualified type name `Workers` in `app_gen.go` can only be
// resolved by a sibling .go file with the same `package app` clause.
// Cross-package references (e.g. `foo.Workers`) are qualified and would
// be flagged by `go build` with a different error class; they're out of
// scope here.
package cli

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// The manifest walks + AST type-collection that used to live inline
// here now flow through checksums.Inspector: DisownedGoFilesByDir,
// GoSiblingsIn, DeclaredTypesIn. The unqualified-reference walker
// (extractUnqualifiedTypeRefs) stays local because it is the only
// caller that needs it and the AST visitor is not shareable with the
// rename-detection or scaffolds-lint code paths.

// danglingFinding identifies one unqualified type reference in a
// forge-owned Tier-1 file that resolves to nothing in the same Go
// package — neither in the file itself, nor in any sibling file
// (disowned or otherwise).
type danglingFinding struct {
	// TypeName is the unqualified identifier referenced as a type.
	TypeName string
	// RefFile is the project-relative path of the file containing the
	// reference (the freshly regenerated non-forked file).
	RefFile string
	// RefLine is the 1-indexed source line of the first reference.
	RefLine int
	// DisownedSiblings is the list of project-relative paths of
	// disowned files in the same package — the candidates whose frozen
	// content is the most likely culprit. May have multiple entries
	// when a package has more than one disowned sibling.
	DisownedSiblings []string
}

// checkDisownedDanglingRefs is the entry point invoked from the
// generate pipeline. It walks the checksum manifest to find every
// disowned file, groups the disowned entries by their Go package
// directory, and for each such directory parses the regenerated
// forge-owned sibling files looking for package-local type references
// that resolve to nothing.
//
// Returns a single batched error listing every finding when at least
// one dangling reference is detected, or nil otherwise. Non-fatal
// internal errors (a file can't be read, doesn't parse) are skipped
// silently — this check is a safety net layered on top of the
// downstream `go build ./...` validation, not the source of truth for
// what compiles.
//
// The ctx is currently unused but reserved for future cancellation /
// log-correlation plumbing; the function signature matches the GenStep
// shape so it can be plugged in directly.
func checkDisownedDanglingRefs(_ context.Context, projectDir string, cs *checksums.FileChecksums) error {
	if cs == nil || len(cs.Disowned) == 0 {
		return nil
	}
	insp := checksums.NewInspector(projectDir, cs)

	// Group disowned Go files by their parent directory. Each parent
	// directory is one Go package's source dir (Go's one-package-per-
	// directory rule). Sibling files in that same directory are the
	// only candidates that could satisfy a package-local type reference.
	disownedByDir := insp.DisownedGoFilesByDir()
	if len(disownedByDir) == 0 {
		return nil
	}

	var findings []danglingFinding
	// Iterate over directories in sorted order so the batched error is
	// deterministic (Go's map iteration is randomized).
	dirs := make([]string, 0, len(disownedByDir))
	for d := range disownedByDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		disownedFiles := disownedByDir[dir]
		// Snapshot every declared top-level type name in the package's
		// directory by parsing each *.go file under it. The inspector
		// reads + parses the disowned file's on-disk content too — the
		// user's frozen file IS the source of truth for what the package
		// now declares.
		siblingFiles, err := insp.GoSiblingsIn(dir)
		if err != nil {
			// A directory we cannot read is not a place we can detect
			// dangling refs. Silent skip preserves the safety-net
			// contract: errors here must never mask a real build
			// problem that downstream `go build` would catch anyway.
			continue
		}
		declaredTypes := map[string]bool{}
		for _, rel := range siblingFiles {
			for name := range insp.DeclaredTypesIn(rel) {
				declaredTypes[name] = true
			}
		}

		// For each forge-owned Tier-1 Go sibling, parse and look for
		// unqualified type references that resolve to nothing in
		// declaredTypes.
		disownedSet := map[string]bool{}
		for _, f := range disownedFiles {
			disownedSet[f] = true
		}
		for _, relPath := range siblingFiles {
			if disownedSet[relPath] {
				continue
			}
			if !insp.IsTier1(relPath) {
				// Not forge-certified (hand-written file or user-owned
				// Tier-2 scaffold in the same package). Not a Tier-1
				// regen target; skip.
				continue
			}
			refs := extractUnqualifiedTypeRefs(projectDir, relPath)
			for _, ref := range refs {
				if declaredTypes[ref.TypeName] {
					continue
				}
				findings = append(findings, danglingFinding{
					TypeName:         ref.TypeName,
					RefFile:          relPath,
					RefLine:          ref.Line,
					DisownedSiblings: append([]string(nil), disownedFiles...),
				})
			}
		}
	}

	if len(findings) == 0 {
		return nil
	}
	return formatDanglingFindingsError(findings)
}

// typeRef pairs a referenced unqualified type name with the 1-indexed
// source line where it first appears.
type typeRef struct {
	TypeName string
	Line     int
}

// extractUnqualifiedTypeRefs walks the Go AST of projectDir/relPath
// looking for type expressions written as bare identifiers (no `pkg.`
// qualifier). Each such identifier is reported once with the first
// line at which it appears.
//
// Why we filter to bare identifiers:
//
//   - Qualified references (`pkg.Workers`) resolve via Go's import
//     machinery and would fail with a different error class at
//     `go build` time. The disowned-dangling case is specifically about
//     names that the COMPILER would otherwise expect to resolve to a
//     sibling .go file in the same package.
//
//   - Builtins (`string`, `int`, `error`, etc.) and well-known
//     stdlib-shaped names that aren't predeclared but appear in type
//     positions purely as builtins are skipped via a small allowlist
//     (predeclaredTypes). Without it, every type field of type `int`
//     in the regenerated file would surface as a finding.
//
// The walk inspects: struct field types, function parameter / return
// types, type aliases, embedded interface entries, channel element
// types, map key/value types, array element types, and pointer base
// types. Composite-literal expressions (`Workers{...}`) are not
// considered here because they appear in value positions, not type
// positions — Go would report a different diagnostic for those.
func extractUnqualifiedTypeRefs(projectDir, relPath string) []typeRef { //nolint:gocognit // a hand-rolled scan of Go type expressions: one flat, independent branch per type-expression form.
	content, err := os.ReadFile(filepath.Join(projectDir, relPath))
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, content, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}

	seen := map[string]int{} // name -> first line
	visit := func(name string, pos token.Pos) {
		if name == "" || predeclaredTypes[name] {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		line := fset.Position(pos).Line
		seen[name] = line
	}

	// Walk every type-position expression in the file. ast.Inspect
	// gives us a uniform traversal; we filter to the AST nodes that
	// carry a type expression.
	var inspectType func(expr ast.Expr)
	inspectType = func(expr ast.Expr) {
		switch e := expr.(type) {
		case *ast.Ident:
			// Bare identifier in a type position — the case we care about.
			visit(e.Name, e.NamePos)
		case *ast.SelectorExpr:
			// Qualified reference (`pkg.Name`). Skip — see doc.
			return
		case *ast.StarExpr:
			inspectType(e.X)
		case *ast.ArrayType:
			inspectType(e.Elt)
		case *ast.MapType:
			inspectType(e.Key)
			inspectType(e.Value)
		case *ast.ChanType:
			inspectType(e.Value)
		case *ast.FuncType:
			if e.Params != nil {
				for _, fld := range e.Params.List {
					inspectType(fld.Type)
				}
			}
			if e.Results != nil {
				for _, fld := range e.Results.List {
					inspectType(fld.Type)
				}
			}
		case *ast.StructType:
			if e.Fields != nil {
				for _, fld := range e.Fields.List {
					inspectType(fld.Type)
				}
			}
		case *ast.InterfaceType:
			if e.Methods != nil {
				for _, fld := range e.Methods.List {
					inspectType(fld.Type)
				}
			}
		case *ast.Ellipsis:
			inspectType(e.Elt)
		}
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					inspectType(s.Type)
				case *ast.ValueSpec:
					if s.Type != nil {
						inspectType(s.Type)
					}
				}
			}
		case *ast.FuncDecl:
			if d.Recv != nil {
				for _, fld := range d.Recv.List {
					inspectType(fld.Type)
				}
			}
			if d.Type != nil {
				inspectType(d.Type)
			}
			// Function bodies can also carry type expressions (e.g.
			// `var x Workers`); walk them too.
			if d.Body != nil {
				ast.Inspect(d.Body, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.CompositeLit:
						if v.Type != nil {
							inspectType(v.Type)
						}
					case *ast.TypeAssertExpr:
						if v.Type != nil {
							inspectType(v.Type)
						}
					case *ast.ValueSpec:
						if v.Type != nil {
							inspectType(v.Type)
						}
					case *ast.FuncLit:
						if v.Type != nil {
							inspectType(v.Type)
						}
					}
					return true
				})
			}
		}
	}

	out := make([]typeRef, 0, len(seen))
	for name, line := range seen {
		out = append(out, typeRef{TypeName: name, Line: line})
	}
	// Stable order for deterministic findings.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].TypeName < out[j].TypeName
	})
	return out
}

// predeclaredTypes is the set of Go predeclared (and pseudo-predeclared)
// type identifiers we treat as builtins. Including all numeric and
// string types from the spec, plus a handful of names that appear in
// type positions in generated code (`any`, `comparable`).
//
// Predeclared identifiers like `nil`, `true`, `false` are values, not
// types, but their inclusion here is harmless — they never appear as
// the operand of a type expression so the visit would never see them.
var predeclaredTypes = map[string]bool{
	"bool":       true,
	"byte":       true,
	"complex64":  true,
	"complex128": true,
	"error":      true,
	"float32":    true,
	"float64":    true,
	"int":        true,
	"int8":       true,
	"int16":      true,
	"int32":      true,
	"int64":      true,
	"rune":       true,
	"string":     true,
	"uint":       true,
	"uint8":      true,
	"uint16":     true,
	"uint32":     true,
	"uint64":     true,
	"uintptr":    true,
	"any":        true,
	"comparable": true,
}

// stepCheckDisownedDanglingRefs is the GenStep wrapper around
// checkDisownedDanglingRefs. Kept in this file (and not in
// generate_pipeline.go) so the single touch on the pipeline file is
// purely a one-line invocation, isolating this addition from parallel
// edits another agent is making in the same file.
func stepCheckDisownedDanglingRefs(ctx *pipelineContext) error {
	return checkDisownedDanglingRefs(context.Background(), ctx.AbsPath, ctx.Checksums)
}

// formatDanglingFindingsError renders the batched-error message. Each
// dangling type gets its own group with every offending call site and
// the actionable escape hatches.
//
// We group by TypeName so a single missing type referenced from N
// regenerated files surfaces as one group of N call sites rather than
// N independent groups (the user's fix — add the type to the disowned
// file, or re-adopt it — is identical for all N references).
func formatDanglingFindingsError(findings []danglingFinding) error {
	byType := map[string][]danglingFinding{}
	for _, f := range findings {
		byType[f.TypeName] = append(byType[f.TypeName], f)
	}
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)

	var b strings.Builder
	fmt.Fprintf(&b, "Disowned-sibling dangling reference check:\n\n")
	fmt.Fprintf(&b, "%d type name(s) referenced by regenerated Tier-1 files but not defined in any sibling file:\n\n", len(types))
	for _, t := range types {
		group := byType[t]
		// Collect the union of forked siblings across every site that
		// referenced this type — usually they're all the same, but a
		// future multi-package forked layout could differ.
		disownedSet := map[string]bool{}
		for _, f := range group {
			for _, s := range f.DisownedSiblings {
				disownedSet[s] = true
			}
		}
		disowned := make([]string, 0, len(disownedSet))
		for s := range disownedSet {
			disowned = append(disowned, s)
		}
		sort.Strings(disowned)

		fmt.Fprintf(&b, "  • type %s\n", t)
		// Stable sort of call sites — by path, then line.
		sort.Slice(group, func(i, j int) bool {
			if group[i].RefFile != group[j].RefFile {
				return group[i].RefFile < group[j].RefFile
			}
			return group[i].RefLine < group[j].RefLine
		})
		for _, f := range group {
			fmt.Fprintf(&b, "      referenced by %s:%d\n", f.RefFile, f.RefLine)
		}
		if len(disowned) == 1 {
			fmt.Fprintf(&b, "      expected to be defined in: %s (disowned)\n", disowned[0])
		} else {
			fmt.Fprintf(&b, "      expected to be defined in one of (disowned):\n")
			for _, s := range disowned {
				fmt.Fprintf(&b, "        - %s\n", s)
			}
		}
	}
	fmt.Fprintf(&b, "\nA disowned file is user-owned and frozen from forge's perspective (recorded in `.forge/disowned.json`); forge never re-emits it, even with `--force`. The regenerated sibling file above expects a type the disowned file doesn't define — a build break is imminent.\n\n")
	fmt.Fprintf(&b, "Two ways out:\n")
	fmt.Fprintf(&b, "  1. Add the missing declaration to the disowned file — it is your code; bring its declarations forward to match the regenerated siblings.\n")
	fmt.Fprintf(&b, "  2. Re-adopt the file: delete it and re-run `forge generate`. Forge re-emits the current template content and owns the file again. WARNING: your disowned content is discarded — copy anything you want to keep into a user-owned extension point first.\n")
	return fmt.Errorf("%s", b.String())
}
