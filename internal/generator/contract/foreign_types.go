package contract

import (
	"go/ast"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
)

// typedInterface builds the InterfaceDef for the foreign interface `name`
// from its TYPE-CHECKED declaration in pkg, rendered into the consuming
// package's namespace.
//
// This is the path every foreign mock takes when the toolchain could load the
// declaring package, and it exists because the syntax path cannot be made
// right: an interface's syntax does not say what an unaliased import is
// called (nats.go binds `nats`), nor which methods an embedded interface
// contributes. go/types knows both. Every package a signature references is
// registered with aliases under its real path and declared name, which is
// what puts `"github.com/nats-io/nats.go"` in the mock's import block.
//
// It reports ok=false — and the caller falls back to the syntax path — when
// name is not an interface in pkg's scope, or when the toolchain could not
// type the signatures (an invalid type anywhere in them). A partial
// type-check must never produce a mock that silently drops a method.
//
// reachable lists the other interfaces declared in pkg that the signatures
// mention, so an aggregate's accessors (db.Store's Estimates() EstimateStore)
// pull in mocks for what they return.
func typedInterface(pkg *packages.Package, name string, aliases *importAliases) (def InterfaceDef, reachable []string, isIface, ok bool) {
	obj, _ := pkg.Types.Scope().Lookup(name).(*types.TypeName)
	if obj == nil {
		return InterfaceDef{}, nil, false, false
	}
	iface, isInterface := obj.Type().Underlying().(*types.Interface)
	if !isInterface {
		return InterfaceDef{}, nil, false, true
	}

	if !typesValid(iface) {
		return InterfaceDef{}, nil, true, false
	}

	def = InterfaceDef{Name: name, Qualifier: aliases.useNamed(pkg.Types.Path(), pkg.Types.Name())}

	seenReach := map[string]bool{}
	note := func(t types.Type) {
		for _, n := range namedInterfacesIn(t, pkg.Types) {
			if n != name && !seenReach[n] {
				seenReach[n] = true
				reachable = append(reachable, n)
			}
		}
	}
	def.Methods = typedMethods(pkg, iface, aliases, note)
	return def, reachable, true, true
}

// embeddedMethods is the method set an interface embedded from another
// package contributes to a contract interface: `sweep.StorageStore` inside
// `type Service interface { sweep.StorageStore; ... }`. imports is
// contract.go's name → path map; the Resolver must have type-checked the
// path (NewResolver loads every package contract.go embeds from).
//
// It returns nil when the embed cannot be read — the package did not load or
// type-check, or the name is not an interface. The mock is then missing those
// methods and its compile-time interface check names the first one, which is
// the honest outcome: a stand-in that silently compiled would be worse, and a
// generate that refused would block every other package over one dep.
func embeddedMethods(sel *ast.SelectorExpr, imports map[string]string, r *Resolver, aliases *importAliases) []MethodDef {
	alias, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil
	}
	pkg := r.foreignPackage(imports[alias.Name])
	if pkg == nil || pkg.Types == nil {
		return nil
	}
	obj, _ := pkg.Types.Scope().Lookup(sel.Sel.Name).(*types.TypeName)
	if obj == nil {
		return nil
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok || !typesValid(iface) {
		return nil
	}
	return typedMethods(pkg, iface, aliases, func(types.Type) {})
}

// siblingMethods is the method set of the interface `name` declared in
// another file of the contract's own package: pkg is that package,
// type-checked. Nil when it did not load or type, or name is not an
// interface there — the same honest outcome as embeddedMethods.
func siblingMethods(pkg *packages.Package, name string, aliases *importAliases) []MethodDef {
	if pkg == nil || pkg.Types == nil {
		return nil
	}
	obj, _ := pkg.Types.Scope().Lookup(name).(*types.TypeName)
	if obj == nil {
		return nil
	}
	iface, ok := obj.Type().Underlying().(*types.Interface)
	if !ok || !typesValid(iface) {
		return nil
	}
	return typedMethods(pkg, iface, aliases, func(types.Type) {})
}

// dedupeMethodDefs keeps the first method of each name, in order.
func dedupeMethodDefs(methods []MethodDef) []MethodDef {
	seen := make(map[string]bool, len(methods))
	out := methods[:0:0]
	for _, m := range methods {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		out = append(out, m)
	}
	return out
}

// typedMethods renders iface's complete method set — embeds flattened by
// go/types — into the consuming package's namespace, registering every
// package a signature references with aliases. note sees each parameter and
// result type.
func typedMethods(pkg *packages.Package, iface *types.Interface, aliases *importAliases, note func(types.Type)) []MethodDef {
	qualifier := func(p *types.Package) string {
		return aliases.useNamed(p.Path(), p.Name())
	}

	// Declared methods first, in source order — the order the syntax path
	// always emitted, so switching paths does not reshuffle existing mocks.
	// go/types orders ExplicitMethod by Id, so source order is recovered
	// from each method's declared position (export data keeps line and
	// column). Methods that arrive only through embedding follow, in
	// go/types' stable (sorted) order.
	var funcs []*types.Func
	declared := map[string]bool{}
	for i := 0; i < iface.NumExplicitMethods(); i++ {
		m := iface.ExplicitMethod(i)
		funcs = append(funcs, m)
		declared[m.Name()] = true
	}
	if pkg.Fset != nil {
		sort.SliceStable(funcs, func(i, j int) bool {
			a, b := pkg.Fset.Position(funcs[i].Pos()), pkg.Fset.Position(funcs[j].Pos())
			if a.Line != b.Line {
				return a.Line < b.Line
			}
			return a.Column < b.Column
		})
	}
	for i := 0; i < iface.NumMethods(); i++ {
		if m := iface.Method(i); !declared[m.Name()] {
			funcs = append(funcs, m)
		}
	}

	var methods []MethodDef
	for _, fn := range funcs {
		sig, _ := fn.Type().(*types.Signature)
		if sig == nil {
			continue
		}
		md := MethodDef{Name: fn.Name()}
		params := sig.Params()
		for i := 0; i < params.Len(); i++ {
			v := params.At(i)
			note(v.Type())
			variadic := sig.Variadic() && i == params.Len()-1
			typeStr := types.TypeString(v.Type(), qualifier)
			if variadic {
				if slice, isSlice := v.Type().(*types.Slice); isSlice {
					typeStr = "..." + types.TypeString(slice.Elem(), qualifier)
				}
			}
			md.Params = append(md.Params, ParamDef{Name: v.Name(), TypeExpr: typeStr, Variadic: variadic})
		}
		results := sig.Results()
		for i := 0; i < results.Len(); i++ {
			v := results.At(i)
			note(v.Type())
			md.Results = append(md.Results, ParamDef{Name: v.Name(), TypeExpr: types.TypeString(v.Type(), qualifier)})
		}
		methods = append(methods, md)
	}
	return methods
}

// typesValid reports whether every type in iface's method signatures was
// resolved. go/packages hands back partial types for a package that failed
// to load; a signature mentioning an unresolved type renders as "invalid
// type", which would compile into nothing.
func typesValid(iface *types.Interface) bool {
	valid := true
	var walk func(t types.Type, depth int)
	walk = func(t types.Type, depth int) {
		if !valid || t == nil || depth > 32 {
			return
		}
		switch x := t.(type) {
		case *types.Basic:
			if x.Kind() == types.Invalid {
				valid = false
			}
		case *types.Pointer:
			walk(x.Elem(), depth+1)
		case *types.Slice:
			walk(x.Elem(), depth+1)
		case *types.Array:
			walk(x.Elem(), depth+1)
		case *types.Map:
			walk(x.Key(), depth+1)
			walk(x.Elem(), depth+1)
		case *types.Chan:
			walk(x.Elem(), depth+1)
		case *types.Signature:
			for i := 0; i < x.Params().Len(); i++ {
				walk(x.Params().At(i).Type(), depth+1)
			}
			for i := 0; i < x.Results().Len(); i++ {
				walk(x.Results().At(i).Type(), depth+1)
			}
		case *types.Named:
			// A named type is resolved by construction; its type
			// arguments may not be.
			for i := 0; i < x.TypeArgs().Len(); i++ {
				walk(x.TypeArgs().At(i), depth+1)
			}
		case *types.Alias:
			walk(types.Unalias(x), depth+1)
		}
	}
	for i := 0; i < iface.NumMethods(); i++ {
		walk(iface.Method(i).Type(), 0)
	}
	return valid
}

// namedInterfacesIn returns the names of interfaces declared in pkg that t
// mentions, at any depth (pointer, slice, map, func, type argument).
func namedInterfacesIn(t types.Type, pkg *types.Package) []string {
	var out []string
	var walk func(t types.Type, depth int)
	walk = func(t types.Type, depth int) {
		if t == nil || depth > 32 {
			return
		}
		switch x := t.(type) {
		case *types.Named:
			if obj := x.Obj(); obj != nil && obj.Pkg() == pkg {
				if _, isIface := x.Underlying().(*types.Interface); isIface {
					out = append(out, obj.Name())
				}
			}
			for i := 0; i < x.TypeArgs().Len(); i++ {
				walk(x.TypeArgs().At(i), depth+1)
			}
		case *types.Alias:
			walk(types.Unalias(x), depth+1)
		case *types.Pointer:
			walk(x.Elem(), depth+1)
		case *types.Slice:
			walk(x.Elem(), depth+1)
		case *types.Array:
			walk(x.Elem(), depth+1)
		case *types.Map:
			walk(x.Key(), depth+1)
			walk(x.Elem(), depth+1)
		case *types.Chan:
			walk(x.Elem(), depth+1)
		case *types.Signature:
			for i := 0; i < x.Params().Len(); i++ {
				walk(x.Params().At(i).Type(), depth+1)
			}
			for i := 0; i < x.Results().Len(); i++ {
				walk(x.Results().At(i).Type(), depth+1)
			}
		}
	}
	walk(t, 0)
	return out
}
