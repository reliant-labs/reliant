package contract

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// ExportedVarsAnalyzer checks for exported package-level variables,
// allowing idiomatic exceptions like sentinel errors and compile-time
// interface checks.
var ExportedVarsAnalyzer = &analysis.Analyzer{
	Name:     "exportedvars",
	Doc:      "checks for exported package-level variables that should be methods or getter functions",
	Run:      runExportedVars,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func init() {
	registerExcludeFlag(&ExportedVarsAnalyzer.Flags)
}

func runExportedVars(pass *analysis.Pass) (interface{}, error) {
	// Honor forge.yaml's contracts.exclude AND the per-package
	// //forge:exclude-contract header — packages opted out of contract
	// enforcement (e.g. //go:embed wrappers, global metric collectors)
	// should not be flagged by EITHER opt-out source.
	if IsExcludedPass(pass) {
		return nil, nil
	}

	// Build a set of vars that are //go:embed targets. The inspector strips
	// directive-only comments from genDecl.Doc, so we walk file-level decls
	// directly to check for the //go:embed directive that must immediately
	// precede the var declaration (per the embed package contract).
	embedTargets := map[*ast.ValueSpec]bool{}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			if !hasEmbedDirective(gd) {
				continue
			}
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					embedTargets[vs] = true
				}
			}
		}
	}

	// Generated files are exempt (see generated.go): forge codegen
	// deliberately exports package vars the rule forbids in user code —
	// the documented `<Entity>Columns` allowlist in Tier-1 `_orm.go`
	// files, the data-only `Inventory` descriptor in mounts_services.go.
	// Those shapes are forge's responsibility; the user cannot durably
	// change them. Test files are exempt too (see testfiles.go): a var in
	// one exists only in the test binary. The skip is per-FILE, so
	// hand-written vars elsewhere in the same package are still flagged.
	exempt := exemptFilenames(pass)

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{(*ast.GenDecl)(nil)}
	insp.Preorder(nodeFilter, func(n ast.Node) {
		genDecl := n.(*ast.GenDecl)
		if genDecl.Tok != token.VAR {
			return
		}
		if exempt[pass.Fset.Position(genDecl.Pos()).Filename] {
			return
		}

		for _, spec := range genDecl.Specs {
			valueSpec := spec.(*ast.ValueSpec)
			for i, name := range valueSpec.Names {
				if !name.IsExported() {
					continue
				}

				// Exception: var _ Interface = (*Type)(nil) — compile-time interface check
				if name.Name == "_" {
					continue
				}

				// Exception: an Err-named sentinel of error type.
				if isSentinelError(pass, name.Name, valueSpec, i) {
					continue
				}

				// Exception: //go:embed targets are inherently package vars —
				// the embed package requires the var to be at file scope and
				// directly preceded by a //go:embed directive. There is no
				// way to expose an embed.FS through a getter without copying
				// it, so flagging these is a false positive.
				if embedTargets[valueSpec] {
					continue
				}

				// Exception: kubebuilder / controller-runtime API group
				// convention. Operators MUST expose `GroupVersion`,
				// `SchemeBuilder`, and `AddToScheme` as package-level vars
				// because controller-runtime's scheme registration
				// (`AddToScheme(scheme)`) and the codegen tooling discover
				// them by name. Wrapping these in a getter would break the
				// k8s API tooling contract.
				if isKubebuilderAPIVar(name.Name, valueSpec, i) {
					continue
				}

				// Exception: const-shaped immutable data — a lookup table,
				// a fixed vocabulary, or a compiled regexp. See
				// isConstShapedData for why a getter buys nothing here.
				if isConstShapedData(pass, valueSpec, i) {
					continue
				}

				pass.Reportf(name.Pos(),
					"exported package variable %s should be a method on a struct or a getter function",
					name.Name)
			}
		}
	})

	return nil, nil
}

// hasEmbedDirective reports whether the given var GenDecl is preceded by a
// //go:embed compiler directive. The directive must appear in the doc comment
// group immediately above the declaration (per the standard library "embed"
// package contract).
func hasEmbedDirective(gd *ast.GenDecl) bool {
	if gd.Doc == nil {
		return false
	}
	for _, c := range gd.Doc.List {
		if strings.HasPrefix(c.Text, "//go:embed ") || c.Text == "//go:embed" {
			return true
		}
	}
	return false
}

// errorInterface is the universe `error` type, the thing this rule's one
// substantive exception is actually about.
var errorInterface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

// isSentinelError reports whether a variable is an idiomatic error
// sentinel: an `Err`-prefixed package var whose TYPE implements error.
//
// The test is the type, not the constructor. Matching on the callee —
// `errors.New` / `fmt.Errorf` and nothing else — put forge's own guidance
// in conflict with forge's own lint: the `service-layer` and `api` skills
// tell an author to build domain errors from `forge/pkg/svcerr`
// (`svcerr.ResourceExhausted("seat cap")`, `svcerr.ErrNotFound`) rather
// than re-roll them, and every one of those shapes was flagged. So were
// `errors.Join`, an aliased `errors` import, and a typed struct sentinel.
// Only `pkg/svcerr` itself passed, and only because it happens to use the
// two literal spellings the whitelist knew.
//
// Type information makes the exception mean what its name says, and it
// stays narrow: `var ErrX = "boom"` is still an exported package var and
// is still reported.
func isSentinelError(pass *analysis.Pass, name string, spec *ast.ValueSpec, idx int) bool {
	if !strings.HasPrefix(name, "Err") {
		return false
	}
	if pass.TypesInfo == nil {
		return false
	}
	// `var ErrX error = f()` states the type outright; otherwise it is the
	// initializer's type. A bare `var ErrX` with neither is not a sentinel.
	var typ types.Type
	switch {
	case spec.Type != nil:
		typ = pass.TypesInfo.TypeOf(spec.Type)
	case idx < len(spec.Values):
		typ = pass.TypesInfo.TypeOf(spec.Values[idx])
	}
	if typ == nil || typ == types.Typ[types.Invalid] {
		return false
	}
	return types.Implements(typ, errorInterface)
}

// isKubebuilderAPIVar returns true when the variable matches one of the
// three package-level vars that controller-runtime (and kubebuilder)
// require operators to expose verbatim:
//
//	GroupVersion   = schema.GroupVersion{...}
//	SchemeBuilder  = runtime.NewSchemeBuilder(...)
//	AddToScheme    = SchemeBuilder.AddToScheme
//
// These are discovered by name by the k8s API machinery, so wrapping them
// in a getter would silently break operator registration.
func isKubebuilderAPIVar(name string, spec *ast.ValueSpec, idx int) bool {
	switch name {
	case "GroupVersion":
		// Initializer must be schema.GroupVersion{...}
		if idx >= len(spec.Values) {
			return false
		}
		cl, ok := spec.Values[idx].(*ast.CompositeLit)
		if !ok {
			return false
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		return ident.Name == "schema" && sel.Sel.Name == "GroupVersion"
	case "SchemeBuilder":
		// Initializer must be runtime.NewSchemeBuilder(...) (kubebuilder
		// classic style) or &scheme.Builder{...} (controller-runtime style
		// emitted by `kubebuilder create api` when the project layout
		// uses sigs.k8s.io/controller-runtime/pkg/scheme).
		if idx >= len(spec.Values) {
			return false
		}
		switch v := spec.Values[idx].(type) {
		case *ast.CallExpr:
			return isCallTo(v, "runtime", "NewSchemeBuilder")
		case *ast.UnaryExpr:
			// &scheme.Builder{...}
			if v.Op != token.AND {
				return false
			}
			cl, ok := v.X.(*ast.CompositeLit)
			if !ok {
				return false
			}
			sel, ok := cl.Type.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return false
			}
			return ident.Name == "scheme" && sel.Sel.Name == "Builder"
		}
		return false
	case "AddToScheme":
		// Initializer must be a selector ending in `.AddToScheme` —
		// typically `SchemeBuilder.AddToScheme`. This is a method value,
		// not a call, so check for a SelectorExpr whose Sel is AddToScheme.
		if idx >= len(spec.Values) {
			return false
		}
		sel, ok := spec.Values[idx].(*ast.SelectorExpr)
		if !ok {
			return false
		}
		return sel.Sel.Name == "AddToScheme"
	}
	return false
}

// isConstShapedData reports whether the variable at spec.Names[idx] is
// immutable data that Go simply cannot spell as a const: a slice or map
// LITERAL of basic values (a fixed vocabulary / lookup table), a
// package-level compiled `regexp.MustCompile(...)`, or a string built by
// a call such as fmt.Sprintf over consts.
//
// Why these are not what the rule is about. The rule's premise is that a
// mutable exported global is state a caller can reach behind the API's
// back, and a method or getter restores the boundary. That reasoning does
// not transfer here:
//
//   - A getter returning a slice or map returns the SAME header. Callers
//     can still mutate the backing array or the map. It hides nothing;
//     the only way a getter would help is by deep-copying on every call,
//     which nobody writes and no caller wants.
//   - `regexp.MustCompile` cannot be a const, and moving it into a getter
//     either recompiles the pattern per call or needs a package-level
//     cache var — the very thing being flagged.
//
// Deliberately narrow: only LITERAL composites of basic element types
// qualify, so a slice of mutable structs, or a map whose values are
// pointers/interfaces/funcs, is still reported. A var assigned from
// another variable is not matched either.
func isConstShapedData(pass *analysis.Pass, spec *ast.ValueSpec, idx int) bool {
	if idx >= len(spec.Values) {
		return false // `var X []string` with no initializer: mutable, report it
	}
	switch v := spec.Values[idx].(type) {
	case *ast.CompositeLit:
		return isBasicElemComposite(pass, v)
	case *ast.CallExpr:
		// regexp.MustCompile / regexp.Compile — precompiled patterns.
		if isCallTo(v, "regexp", "MustCompile") || isCallTo(v, "regexp", "Compile") {
			return true
		}
		// A string assembled from constants (fmt.Sprintf and friends):
		// const-shaped in every way except that Go will not fold it.
		return isBasicType(pass.TypesInfo.TypeOf(spec.Values[idx])) &&
			(isCallTo(v, "fmt", "Sprintf") || isCallTo(v, "strings", "Join"))
	}
	return false
}

// isBasicElemComposite reports whether lit is a slice/array/map literal
// whose element (and key, for maps) types are basic — string, numeric,
// bool. That is the "fixed vocabulary" shape; anything holding pointers,
// interfaces, funcs or mutable structs falls through to the rule.
func isBasicElemComposite(pass *analysis.Pass, lit *ast.CompositeLit) bool {
	t := pass.TypesInfo.TypeOf(lit)
	if t == nil {
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Slice:
		return isBasicType(u.Elem())
	case *types.Array:
		return isBasicType(u.Elem())
	case *types.Map:
		return isBasicType(u.Key()) && isBasicType(u.Elem())
	}
	return false
}

// isBasicType reports whether t is a basic (string/numeric/bool) type,
// looking through named types.
func isBasicType(t types.Type) bool {
	if t == nil {
		return false
	}
	_, ok := t.Underlying().(*types.Basic)
	return ok
}

// isCallTo checks if a call expression is pkg.funcName(...).
func isCallTo(call *ast.CallExpr, pkg, funcName string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == pkg && sel.Sel.Name == funcName
}
