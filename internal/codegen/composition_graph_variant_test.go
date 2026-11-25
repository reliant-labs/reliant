package codegen

import (
	"go/token"
	"go/types"
	"testing"
)

// variantOnlyFixture: package workflowcheck has no default configuration
// (its Deps do not resolve, so generate leaves its node out); the only thing
// that builds it is the SyncingChecks variant, a wrapper embedding
// workflowcheck.Service, which the worker root reaches when reachVariant.
func variantOnlyFixture(reachVariant bool) *graphModel {
	wc := types.NewPackage("example.com/x/internal/workflowcheck", "workflowcheck")
	app := types.NewPackage("example.com/x/internal/app", "app")
	svc := types.NewNamed(types.NewTypeName(token.NoPos, wc, "Service", nil), types.NewInterfaceType(nil, nil), nil)
	wrapper := types.NewNamed(types.NewTypeName(token.NoPos, app, "SyncingChecks", nil),
		types.NewStruct([]*types.Var{types.NewField(token.NoPos, app, "Service", svc, true)}, nil), nil)

	pkg := &graphNode{Name: "Workflowcheck", Kind: nodeComponent, Role: "internal", Type: "workflowcheck.Service",
		Doc: "the workflowcheck package", Comp: &BuildComponent{ImportPath: "internal/workflowcheck"}}
	variant := &graphNode{Name: "SyncingChecks", Kind: nodeProvider, Type: "*SyncingChecks", File: "internal/app/variant_syncing_checks.go", Line: 34, RootOnly: []string{"worker"}}
	reach := []string{}
	if reachVariant {
		reach = []string{"SyncingChecks"}
	}
	return &graphModel{
		nodes:  []*graphNode{pkg, variant},
		byName: map[string]*graphNode{"Workflowcheck": pkg, "SyncingChecks": variant},
		u: &graphUniverse{produced: map[string]types.Type{
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
	if fs := variantOnlyFixture(true).unreachableFindings(); len(fs) != 0 {
		t.Fatalf("a package a reached variant wraps is reached through it, got %+v", fs)
	}
	fs := variantOnlyFixture(false).unreachableFindings()
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
