package contract

import (
	"go/ast"
	"go/types"

	"github.com/reliant-labs/forge/internal/contractsrc"
)

// A contract may be ANOTHER package's interface, by alias:
//
//	type Service = tools.ChangeController
//
// That is the shape of an adapter whose contract its consumer owns (reliant's
// internal/changetools implements internal/llm/tools' ChangeController). The
// alias is the honest spelling — the two cannot drift, and there is one
// interface, not two — where the workaround, `type Service interface {
// tools.ChangeController }`, declares a second interface with the same
// method set.
//
// Only the canonical contract name is read this way: any other alias in
// contract.go is not the package's contract, and mocking it would add a
// second mock of someone else's interface. A same-package target
// (`type Service = Gateway`) works too.

// contractInterfaceType is the interface a contract file's type spec
// declares: its own interface type, or a `type Service = <T>` alias read as
// the interface that embeds T. nil for any other type.
func contractInterfaceType(ts *ast.TypeSpec, cf *File, r *Resolver) *ast.InterfaceType {
	if it, ok := ts.Type.(*ast.InterfaceType); ok {
		return it
	}
	return serviceAliasInterface(ts, cf, r)
}

// collectContractInterfaceNames records the interfaces the contract files
// declare, so an alias's target is known whichever order they come in.
func collectContractInterfaceNames(files []*ast.File, names map[string]bool) {
	for _, f := range files {
		collectInterfaceNames(f, names)
	}
}

// serviceAliasTarget is the type a `type Service = <T>` alias spec names, or
// nil when ts is not that alias.
func serviceAliasTarget(ts *ast.TypeSpec) ast.Expr {
	if !ts.Assign.IsValid() || ts.Name.Name != contractsrc.InterfaceName || ts.TypeParams != nil {
		return nil
	}
	switch t := ts.Type.(type) {
	case *ast.SelectorExpr:
		if _, ok := t.X.(*ast.Ident); ok {
			return t
		}
	case *ast.Ident:
		return t
	}
	return nil
}

// serviceAliasInterface renders a `type Service = <T>` alias as the
// interface that embeds T, when T is an interface: the parser then gives the
// mock, decorator and test scaffold T's method set through the embed paths
// it already has. nil when ts is not that alias, or T is not known to be an
// interface — the package then gets no Service mock, as before.
func serviceAliasInterface(ts *ast.TypeSpec, cf *File, r *Resolver) *ast.InterfaceType {
	target := serviceAliasTarget(ts)
	if target == nil {
		return nil
	}
	switch t := target.(type) {
	case *ast.SelectorExpr:
		pkg := r.foreignPackage(cf.Imports[t.X.(*ast.Ident).Name])
		if pkg == nil || pkg.Types == nil {
			return nil
		}
		obj, _ := pkg.Types.Scope().Lookup(t.Sel.Name).(*types.TypeName)
		if obj == nil {
			return nil
		}
		if _, ok := obj.Type().Underlying().(*types.Interface); !ok {
			return nil
		}
	case *ast.Ident:
		// cf.InterfaceNames holds every interface the package declares,
		// in contract.go or another file (collected before this pass).
		if !cf.InterfaceNames[t.Name] {
			return nil
		}
	}
	return &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{Type: target}}}}
}

// serviceAliasEmbeds is the target of every `type Service = <T>` alias in
// file, for the Resolver to type-check: the package a selector names, or the
// contract's own package for a bare name declared in another file.
func serviceAliasEmbeds(file *ast.File) []ast.Expr {
	var out []ast.Expr
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				if t := serviceAliasTarget(ts); t != nil {
					out = append(out, t)
				}
			}
		}
	}
	return out
}
