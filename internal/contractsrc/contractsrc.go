// Package contractsrc answers one question for every part of forge that
// needs it: where does this package's Service contract come from?
//
// A package declares its contract in one of two forms:
//
//   - hand-written: `type Service interface {...}` in contract.go. The
//     original form, and the right one for a strategy interface with
//     several implementations, where the interface IS the abstraction.
//   - derived: `//forge:contract` on the implementing type (the unexported
//     `service` struct, usually). `forge generate` writes contract_gen.go
//     holding `type Service interface` with every exported method of that
//     type, so the interface is never written twice.
//
// Everything that used to ask "does dir have a contract.go?" — the
// instrument decision, component discovery, the contract-gap lint, retire,
// explain — asks HasContract instead, so a marked type counts exactly like a
// contract.go. Everything that PARSES the interface reads Path: contract.go,
// or contract_gen.go for the derived form.
//
// Like pkgdirective, this package imports only the standard library (and
// pkgdirective): internal/tools/mockgen reaches it through
// internal/generator/contract and must build without forge's generated code.
package contractsrc

import (
	"bytes"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/pkgdirective"
)

const (
	// FileName is the hand-written contract file.
	FileName = "contract.go"
	// DerivedFileName is the forge-owned file `forge generate` writes the
	// Service interface to from a `//forge:contract` type.
	DerivedFileName = "contract_gen.go"
	// Marker is the directive that, on a type that is not an interface,
	// makes its exported method set the package's contract. On an
	// interface it keeps its original meaning: "this interface is the
	// contract, under a non-canonical name".
	Marker = "forge:contract"
	// InterfaceName is the name of the derived interface.
	InterfaceName = "Service"
)

// MarkedType is a type declaration carrying `//forge:contract` that is not
// an interface: the implementation the contract is derived from.
type MarkedType struct {
	// Name is the type's name (conventionally the unexported `service`).
	Name string
	// File is the base name of the file declaring it.
	File string
	// Line is the declaration's 1-indexed line.
	Line int
	// Generic is true for a type with type parameters, which cannot
	// satisfy a non-generic interface by itself: derivation refuses it.
	Generic bool
	// Alias is true for `type x = y`, which declares no methods of its
	// own: derivation refuses it.
	Alias bool
	// Constrained is true when the declaring file builds only under some
	// targets (a //go:build line or a _GOOS/_GOARCH name): derivation
	// refuses it, since contract_gen.go is built everywhere.
	Constrained bool
}

// HandWritten is a hand-written contract interface found in a package: one
// named Service, or one carrying `//forge:contract` / `//forge:service`.
type HandWritten struct {
	Name string
	File string
	Line int
}

// IsContractFile reports whether base (a bare file name) is a file a
// package's contract interface is read from: contract.go or the derived
// contract_gen.go.
func IsContractFile(base string) bool {
	return base == FileName || base == DerivedFileName
}

// HasContract reports whether the package in dir declares a Service
// contract in either form: a contract.go, or a `//forge:contract` type.
// It does not require contract_gen.go to exist yet — a package is a
// contract package from the moment it is marked, before the first
// `forge generate`.
func HasContract(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		return true
	}
	return HasMarkedType(dir)
}

// HasMarkedType reports whether dir declares a `//forge:contract` type that
// is not an interface.
func HasMarkedType(dir string) bool {
	return len(MarkedTypes(dir)) > 0
}

// Path returns the file the package's contract interface is read from: the
// derived contract_gen.go when the package has a `//forge:contract` type,
// else contract.go when it exists, else "". The derived path is returned
// whether or not it has been written yet; `forge generate` writes it before
// anything reads it.
func Path(dir string) string {
	if HasMarkedType(dir) {
		return filepath.Join(dir, DerivedFileName)
	}
	p := filepath.Join(dir, FileName)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// MarkedTypes returns every non-interface type in dir's non-test,
// non-generated files carrying `//forge:contract`, in file-name then line
// order. More than one is an error derivation reports.
func MarkedTypes(dir string) []MarkedType {
	var out []MarkedType
	eachSourceFile(dir, func(name string, fset *token.FileSet, file *ast.File) {
		eachTypeSpec(file, func(gd *ast.GenDecl, ts *ast.TypeSpec) {
			if _, isIface := ts.Type.(*ast.InterfaceType); isIface {
				return
			}
			if !hasDirective(Marker, gd, ts) {
				return
			}
			out = append(out, MarkedType{
				Name:        ts.Name.Name,
				File:        name,
				Line:        fset.Position(ts.Pos()).Line,
				Generic:     ts.TypeParams != nil && len(ts.TypeParams.List) > 0,
				Alias:       ts.Assign.IsValid(),
				Constrained: BuildConstrained(dir, name, file),
			})
		})
	})
	return out
}

// HandWrittenContracts returns the hand-written contract interfaces in dir's
// non-test, non-generated files: an interface named Service, or one carrying
// `//forge:contract` / `//forge:service`. A package that has one AND a
// marked type declares its contract twice, which derivation and lint refuse.
func HandWrittenContracts(dir string) []HandWritten {
	var out []HandWritten
	eachSourceFile(dir, func(name string, fset *token.FileSet, file *ast.File) {
		eachTypeSpec(file, func(gd *ast.GenDecl, ts *ast.TypeSpec) {
			if _, isIface := ts.Type.(*ast.InterfaceType); !isIface {
				return
			}
			if ts.Name.Name != InterfaceName && !hasDirective(Marker, gd, ts) && !hasDirective("forge:service", gd, ts) {
				return
			}
			out = append(out, HandWritten{Name: ts.Name.Name, File: name, Line: fset.Position(ts.Pos()).Line})
		})
	})
	return out
}

// HasDirectiveComment reports whether any comment in groups is exactly
// directive (spaced or unspaced, whole line) — the rule every forge marker
// follows, so prose that mentions the marker is never mistaken for it.
func HasDirectiveComment(directive string, groups ...*ast.CommentGroup) bool {
	for _, cg := range groups {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			if pkgdirective.TrimCommentMarkers(c.Text) == directive {
				return true
			}
		}
	}
	return false
}

// hasDirective reads a type's three comment slots: the GenDecl doc (a
// single `type x ...`), the spec doc (inside `type (...)`) and the inline
// comment.
func hasDirective(directive string, gd *ast.GenDecl, ts *ast.TypeSpec) bool {
	// A grouped declaration's doc describes the group, not each member.
	var declDoc *ast.CommentGroup
	if len(gd.Specs) == 1 {
		declDoc = gd.Doc
	}
	return HasDirectiveComment(directive, declDoc, ts.Doc, ts.Comment)
}

// anyTarget is a build context matching no real GOOS or GOARCH, so a file
// it excludes by name (x_linux.go, x_arm64.go, or a name the go tool
// ignores) is one some build leaves out.
var anyTarget = func() build.Context {
	c := build.Default
	c.GOOS, c.GOARCH = "forgeanyos", "forgeanyarch"
	c.CgoEnabled = true
	return c
}()

// BuildConstrained reports whether the file name in dir, parsed as file
// (with comments), is in some builds of its package and not others: it
// carries a //go:build (or // +build) line, or its name has a GOOS/GOARCH
// suffix. Discovery reads every file whatever the target, so a contract
// type or method declared in such a file is refused rather than derived
// into a contract_gen.go that builds everywhere.
func BuildConstrained(dir, name string, file *ast.File) bool {
	for _, cg := range file.Comments {
		if cg.Pos() >= file.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				return true
			}
		}
	}
	ok, err := anyTarget.MatchFile(dir, name)
	return err == nil && !ok
}

// SourceFiles returns the base names of dir's non-test, non-generated .go
// files, sorted.
func SourceFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") ||
			strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// markerBytes is the cheap pre-filter: a file that never spells a forge
// contract directive or a Service type is not parsed.
var markerBytes = [][]byte{[]byte("forge:contract"), []byte("forge:service"), []byte("Service")}

func eachSourceFile(dir string, fn func(name string, fset *token.FileSet, file *ast.File)) {
	fset := token.NewFileSet()
	for _, name := range SourceFiles(dir) {
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		relevant := false
		for _, m := range markerBytes {
			if bytes.Contains(src, m) {
				relevant = true
				break
			}
		}
		if !relevant {
			continue
		}
		file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		fn(name, fset, file)
	}
}

func eachTypeSpec(file *ast.File, fn func(gd *ast.GenDecl, ts *ast.TypeSpec)) {
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				fn(gd, ts)
			}
		}
	}
}
