package closedset

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// NewResolver resolves declaration names for the project at dir: a short
// name from the registry of forge-exported sets, a qualified one from the
// project's own Go source (ResolveGo). Each package is loaded once.
func NewResolver(dir string) Resolver {
	cache := map[string][]string{}
	return func(name string) ([]string, error) {
		if !IsQualified(name) {
			if s, ok := Lookup(name); ok {
				return s.Values, nil
			}
			return nil, fmt.Errorf("forge:closed-set %s names no set forge exports — forge exports %s; a set your project owns is named by its qualified Go name, `<import path>.<Var>` (e.g. github.com/acme/app/internal/billing.AllTiers)",
				name, strings.Join(Names(), ", "))
		}
		if v, ok := cache[name]; ok {
			return v, nil
		}
		v, err := ResolveGo(dir, name)
		if err != nil {
			return nil, fmt.Errorf("forge:closed-set %s: %w", name, err)
		}
		cache[name] = v
		return v, nil
	}
}

// ResolveGo reads a project-owned closed set: qualified is
// `<import path>.<Var>`, a package-level variable whose initializer is a
// composite literal (`[]Tier{TierFree, TierPro}`, `[...]string{"a", "b"}`)
// of string or integer constants. The package is type-checked from source in
// dir's module and the members are the constants' values, so a member named
// by a constant (`TierFree`) reads as the value it stands for. Nothing in the
// project is executed.
//
// A variable initialized any other way — from a function call, from another
// variable — is refused with the reason: forge reads values it can prove
// from the source, and names the literal to point at instead.
func ResolveGo(dir, qualified string) ([]string, error) {
	dot := strings.LastIndexByte(qualified, '.')
	if dot <= strings.LastIndexByte(qualified, '/') {
		return nil, fmt.Errorf("a qualified set name is `<import path>.<Var>`, and %q has no `.<Var>` after its import path", qualified)
	}
	path, name := qualified[:dot], qualified[dot+1:]
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  dir,
		Env:  goexec.Env(),
	}, path)
	if err != nil {
		return nil, fmt.Errorf("load package %s: %w", path, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("package %s resolved to %d packages", path, len(pkgs))
	}
	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		return nil, fmt.Errorf("package %s does not load: %v", path, pkg.Errors[0])
	}
	obj, ok := pkg.Types.Scope().Lookup(name).(*types.Var)
	if !ok {
		return nil, fmt.Errorf("package %s declares no package-level variable %s", path, name)
	}
	init := initializer(pkg, obj)
	if init == nil {
		return nil, fmt.Errorf("%s has no initializer forge can read — declare it `var %s = []T{…}`", qualified, name)
	}
	lit, ok := ast.Unparen(init).(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("%s is not initialized by a literal (it is %s) — name the variable that holds the literal list", qualified, types.ExprString(init))
	}
	var out []string
	for i, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			elt = kv.Value
		}
		tv := pkg.TypesInfo.Types[elt]
		switch {
		case tv.Value == nil:
			return nil, fmt.Errorf("member %d of %s (%s) is not a constant", i+1, qualified, types.ExprString(elt))
		case tv.Value.Kind() == constant.String:
			out = append(out, constant.StringVal(tv.Value))
		case tv.Value.Kind() == constant.Int:
			out = append(out, tv.Value.ExactString())
		default:
			return nil, fmt.Errorf("member %d of %s (%s) is a %s constant; a closed set holds strings or integers", i+1, qualified, types.ExprString(elt), tv.Value.Kind())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s is empty", qualified)
	}
	return out, nil
}

// initializer finds the expression obj is declared with.
func initializer(pkg *packages.Package, obj *types.Var) ast.Expr {
	for _, f := range pkg.Syntax {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if pkg.TypesInfo.Defs[n] == obj && i < len(vs.Values) && len(vs.Values) == len(vs.Names) {
						return vs.Values[i]
					}
				}
			}
		}
	}
	return nil
}
