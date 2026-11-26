package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// variantOnlyFixture: package workflowcheck has no default configuration
// (its Deps do not resolve, so generate leaves its node out); the only thing
// that builds it is the SyncingChecks variant, a wrapper embedding
// workflowcheck.Service, which the worker root reaches when reachVariant.
func variantOnlyFixture(t *testing.T, reachVariant bool) *graphModel {
	t.Helper()
	return variantOnlyFixtureCtor(t, reachVariant, "New")
}

// variantOnlyFixtureCtor is variantOnlyFixture with the variant built by
// workflowcheck.<ctor>.
func variantOnlyFixtureCtor(t *testing.T, reachVariant bool, ctor string) *graphModel {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "variant.go", `package app
func NewSyncingChecks() { return &SyncingChecks{Service: workflowcheck.`+ctor+`()} }
`, 0)
	if err != nil {
		t.Fatal(err)
	}
	wc := types.NewPackage("example.com/x/internal/workflowcheck", "workflowcheck")
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == ctor {
			info.Uses[sel.Sel] = types.NewFunc(token.NoPos, wc, ctor, nil)
		}
		return true
	})
	app := types.NewPackage("example.com/x/internal/app", "app")
	svc := types.NewNamed(types.NewTypeName(token.NoPos, wc, "Service", nil), types.NewInterfaceType(nil, nil), nil)
	wrapper := types.NewNamed(types.NewTypeName(token.NoPos, app, "SyncingChecks", nil),
		types.NewStruct([]*types.Var{types.NewField(token.NoPos, app, "Service", svc, true)}, nil), nil)

	pkg := &graphNode{Name: "Workflowcheck", Kind: nodeComponent, Role: "internal", Type: "workflowcheck.Service",
		Doc: "the workflowcheck package", Comp: &BuildComponent{ImportPath: "internal/workflowcheck", compConstructor: "New"}}
	variant := &graphNode{Name: "SyncingChecks", Kind: nodeProvider, Type: "*SyncingChecks", File: "internal/app/variant_syncing_checks.go", Line: 34, RootOnly: []string{"worker"}}
	reach := []string{}
	if reachVariant {
		reach = []string{"SyncingChecks"}
	}
	return &graphModel{
		nodes:  []*graphNode{pkg, variant},
		byName: map[string]*graphNode{"Workflowcheck": pkg, "SyncingChecks": variant},
		u: &graphUniverse{app: &packages.Package{Syntax: []*ast.File{file}, TypesInfo: info}, produced: map[string]types.Type{
			"Workflowcheck": svc,
			"SyncingChecks": types.NewPointer(wrapper),
		}},
		unbuilt:   map[string]unbuiltNode{"Workflowcheck": {Problems: []graphProblem{{Node: "Workflowcheck", Field: "GitHub", Type: "workflowcheck.GitHub"}}}},
		roots:     []graphRoot{{Name: "worker", TypeName: "Worker"}},
		rootViews: []graphRootView{{Reach: reach}},
		edges:     map[string][]string{},
		lazy:      map[string][]string{},
	}
}

func TestVariantOnlyPackageIsNotUnreachable(t *testing.T) {
	if fs := variantOnlyFixture(t, true).unreachableFindings(); len(fs) != 0 {
		t.Fatalf("a package a reached variant wraps is reached through it, got %+v", fs)
	}
	fs := variantOnlyFixture(t, false).unreachableFindings()
	var pkgFinding bool
	for _, f := range fs {
		if f.Rule == GraphRuleUnreachable && f.File == "internal/workflowcheck" && f.Error {
			pkgFinding = true
		}
	}
	if !pkgFinding {
		t.Fatalf("with no root reaching a variant the package stays unreachable, got %+v", fs)
	}
}

// A variant built with the generated NewInstrumented (New wrapped in the
// package's middleware) reaches the package as New does; reliant's
// workflowcheck variants are built that way.
func TestVariantBuiltWithNewInstrumentedReachesPackage(t *testing.T) {
	if fs := variantOnlyFixtureCtor(t, true, "NewInstrumented").unreachableFindings(); len(fs) != 0 {
		t.Fatalf("a variant built with NewInstrumented reaches the package, got %+v", fs)
	}
	if fs := variantOnlyFixtureCtor(t, true, "NewOther").unreachableFindings(); len(fs) == 0 {
		t.Fatal("an unrelated constructor is not reach evidence")
	}
}
