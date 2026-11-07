// File: internal/codegen/compose_constructible.go
//
// Whether compose.go can construct a component at all, and the refusal
// generate prints when it cannot.
//
// compose.go constructs every composed component the same way:
//
//	c.Foo = foo.New(foo.Deps{…})              // or
//	foo, err := foo.New(foo.Deps{…})
//
// That line is only valid Go when the package declares a `Deps` struct and a
// constructor that takes exactly that Deps and returns the component (or the
// component and an error). The pre-codegen contract check enforces this shape
// on most contract packages, but it deliberately stays silent on two shapes it
// judges "not a Service-shape package": a contract.go with no interface, and an
// interface catalogue with no Deps and no constructor. Composition still
// composed both, emitted a call that could not compile, and the validate build
// then blamed internal/app/compose.go — a file forge had just written.
//
// So the composer enforces its own invariant: it never emits a constructor
// call it cannot type. It refuses, naming each package and exactly what is
// wrong with its constructor, rather than leaving the package out. Leaving it
// out would be inferring membership, which compose_membership.go rules out: a
// package whose constructor broke in a refactor would silently vanish from the
// server, and its consumers would then fail with a "no provider" error that
// steers the author to hand-build it on Infra. Membership stays declared, by
// the markers the refusal names.
//
// The check lives here rather than in the pre-codegen contract check because
// only composition knows the exact membership (after the external-component
// and not-a-component markers) and the constructor it will call
// (DetectConstructorName), and because a check here cannot be skipped with
// --skip-pre-checks. Generate journals its writes, so a refusal here still
// leaves the tree as it found it.

package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// unconstructibleComponent is one composed component compose.go could not
// construct, with every reason it could not.
type unconstructibleComponent struct {
	Dir      string   // module-relative package dir ("internal/foo")
	RoleRoot string   // "internal", "internal/handlers", "internal/workers", "internal/operators"
	Problems []string // each one names the declaration it is about
}

// unconstructibleComponents returns every composed component whose
// constructor compose.go could not call, in Dir order.
func unconstructibleComponents(projectDir string, composed []BuildComponent) []unconstructibleComponent {
	var out []unconstructibleComponent
	for _, c := range composed {
		rel := filepath.Join(filepath.FromSlash(c.compRoleRoot), filepath.FromSlash(c.compImportLeaf))
		problems := composeConstructorProblems(projectDir, filepath.Join(projectDir, rel), c.compPackage)
		if len(problems) == 0 {
			continue
		}
		out = append(out, unconstructibleComponent{
			Dir:      filepath.ToSlash(rel),
			RoleRoot: c.compRoleRoot,
			Problems: problems,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// composeConstructorProblems reports why compose.go could not emit
// `<pkg>.<Ctor>(<pkg>.Deps{…})` for the package in dir, or nil when it can.
//
// It reads the same constructor name compose emits (DetectConstructorName),
// and it answers only "would that call compile?" — not whether the package
// follows the canonical contract shape, which is the pre-codegen check's
// question. So `func New(Deps) *Client` with no interface passes here: compose
// types the field off the constructor's return, and that compiles.
//
// It returns nil, rather than guessing, whenever it cannot see the whole
// package: a directory that is not on disk yet, or a file that does not parse
// (the compiler then names the syntax error in the author's own file, which is
// the better message).
func composeConstructorProblems(projectDir, dir, pkg string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil
	}
	if pkg == "" {
		pkg = files[0].Name.Name
	}

	ctorName := DetectConstructorName(dir)
	var (
		depsSpec *ast.TypeSpec
		ctor     *ast.FuncDecl
		ctorVar  bool // `var New = func(…)`: callable, but not a decl we can read
	)
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if s.Name.Name == "Deps" && depsSpec == nil {
							depsSpec = s
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.Name == ctorName {
								ctorVar = true
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name != nil && d.Name.Name == ctorName && ctor == nil {
					ctor = d
				}
			}
		}
	}

	at := func(pos token.Pos) string {
		p := fset.Position(pos)
		if rel, relErr := filepath.Rel(projectDir, p.Filename); relErr == nil {
			return fmt.Sprintf("%s:%d", filepath.ToSlash(rel), p.Line)
		}
		return fmt.Sprintf("%s:%d", p.Filename, p.Line)
	}
	call := fmt.Sprintf("%s.%s(%s.Deps{…})", pkg, ctorName, pkg)

	var problems []string
	switch {
	case depsSpec == nil:
		problems = append(problems, fmt.Sprintf("declares no `type Deps struct`, and compose.go calls %s", call))
	case isInterfaceType(depsSpec.Type):
		problems = append(problems, fmt.Sprintf("%s: `Deps` is an interface, and compose.go builds it as a struct literal in %s",
			at(depsSpec.Pos()), call))
	}

	if ctor == nil {
		if ctorVar {
			return problems
		}
		if ctorName == DefaultConstructorName {
			problems = append(problems, "declares no constructor: no `func New`, and no func marked `// forge:constructor`")
		} else {
			problems = append(problems, fmt.Sprintf("declares no func %#q", ctorName))
		}
		return problems
	}

	sig := printType(fset, ctor.Type)
	where := fmt.Sprintf("%s: %#q is %#q", at(ctor.Pos()), ctorName, sig)
	if ctor.Type.TypeParams != nil && len(ctor.Type.TypeParams.List) > 0 {
		problems = append(problems, fmt.Sprintf("%s — it has type parameters, which %s cannot infer", where, call))
	}
	if !takesDepsOnly(ctor.Type.Params) {
		problems = append(problems, fmt.Sprintf("%s — compose.go calls %s, so its only parameter must be `Deps`", where, call))
	}
	if problem := constructorResultsProblem(ctor.Type.Results); problem != "" {
		problems = append(problems, fmt.Sprintf("%s — %s; compose.go needs the component, or the component and an error", where, problem))
	}
	return problems
}

// takesDepsOnly reports whether a constructor's parameters accept a single
// `Deps` argument: exactly `(Deps)`, or `(Deps, ...T)`, whose variadic tail
// may be empty.
func takesDepsOnly(params *ast.FieldList) bool {
	types := flattenFieldTypes(params)
	if len(types) == 0 || !isIdent(types[0], "Deps") {
		return false
	}
	switch len(types) {
	case 1:
		return true
	case 2:
		_, variadic := types[1].(*ast.Ellipsis)
		return variadic
	}
	return false
}

// constructorResultsProblem describes a result list compose.go cannot assign
// from, or "" when it is `T` or `(T, error)`.
func constructorResultsProblem(results *ast.FieldList) string {
	types := flattenFieldTypes(results)
	switch {
	case len(types) == 0:
		return "it returns nothing"
	case len(types) == 1:
		return ""
	case len(types) == 2 && isIdent(types[1], "error"):
		return ""
	case len(types) == 2:
		return "its second result is not `error`"
	}
	return fmt.Sprintf("it returns %d values", len(types))
}

// flattenFieldTypes expands a field list to one type per value: `(a, b int)`
// is two ints.
func flattenFieldTypes(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for range n {
			out = append(out, f.Type)
		}
	}
	return out
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}

func isInterfaceType(expr ast.Expr) bool {
	_, ok := expr.(*ast.InterfaceType)
	return ok
}

// unconstructibleError names every component compose.go cannot construct and
// what makes each one constructible, or truthfully not composed.
func unconstructibleError(found []unconstructibleComponent) error {
	var b strings.Builder
	b.WriteString("forge cannot construct these components, so it did not write internal/app/compose.go:\n\n")
	anyInternal, anyRole := false, false
	for _, u := range found {
		fmt.Fprintf(&b, "  - %s\n", u.Dir)
		for _, p := range u.Problems {
			fmt.Fprintf(&b, "      %s\n", p)
		}
		if u.RoleRoot == "internal" {
			anyInternal = true
		} else {
			anyRole = true
		}
	}
	b.WriteString("\ncompose.go constructs every component it composes as `pkg.New(pkg.Deps{…})`, so the package needs a\n")
	b.WriteString("`type Deps struct` and a constructor `func New(Deps) T` or `func New(Deps) (T, error)` — any name\n")
	b.WriteString("works with `// forge:constructor` on the line above it. The canonical T is the package's Service interface.\n")
	if anyInternal {
		b.WriteString("\nFor each internal package, make one of these true:\n")
		b.WriteString("  - the server builds it: give it that Deps and constructor.\n")
		b.WriteString("  - the server has it, built by hand: mark it //forge:external-component and put the instance on Infra\n")
		b.WriteString("    (internal/app/providers.go).\n")
		b.WriteString("  - the server does not have it (the CLI, a daemon or another binary builds it, or it is a catalogue of\n")
		b.WriteString("    interfaces nothing constructs): mark it `//forge:not-a-component: <why>`. It keeps contract.go and its mock.\n")
		b.WriteString("  - it is not a component at all: mark it `//forge:exclude-contract: <why>`, which also drops its generated mock.\n")
		b.WriteString("  (`forge skill load contracts/package-markers`)\n")
	}
	if anyRole {
		b.WriteString("\nA handler, worker or operator is always composed: restore the constructor its scaffold wrote\n")
		b.WriteString("(`func New(deps Deps) (*Service, error)`, `*Worker`, `*Controller`).\n")
	}
	return fmt.Errorf("%s", b.String())
}
