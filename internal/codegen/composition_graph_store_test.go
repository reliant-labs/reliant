package codegen

import (
	"go/types"
	"testing"
)

// The composition graph's store seam follows database.orm.package (F12.2):
// a `store.Store` / `store.<Entity>Store` Deps field resolves to the ORM
// package's NewStore, imported from there — not internal/db.
func TestGraphResolver_StoreSeamFollowsTheORMPackage(t *testing.T) {
	orm := &graphNode{Name: "DB", Type: "*orm.Client", Imports: map[string]string{"orm": "github.com/reliant-labs/forge/pkg/orm"}}
	svc := &graphNode{Name: "Orders"}
	r := &graphResolver{
		module: "example.com/proj",
		nodes:  []*graphNode{orm, svc},
		u:      &graphUniverse{produced: map[string]types.Type{}, deps: map[string]map[string]types.Type{}},
		store: storeSeam{pkg: "store", dir: "internal/store",
			accessors: map[string]string{"store.EstimateStore": "Estimates"}},
	}
	for depType, want := range map[string]string{
		"store.Store":         "store.NewStore(g.DB())",
		"store.EstimateStore": "store.NewStore(g.DB()).Estimates()",
	} {
		got, problem := r.resolve(svc, DepsField{Name: "X", Type: depType})
		if problem != nil {
			t.Fatalf("%s: %+v", depType, problem)
		}
		if got.Expr != want || got.Imports["store"] != "example.com/proj/internal/store" {
			t.Errorf("%s resolved to %q imports %v, want %q from internal/store", depType, got.Expr, got.Imports, want)
		}
	}
	if r.store.owns("db.Store") {
		t.Error("with the ORM in internal/store, db.Store is the project's own type, not the seam's")
	}
}

// An orm.Context Deps field is a graph input (P1): it resolves to the
// project's ORM client node, or to a node exposing it through ORM(); an
// explicit producer of orm.Context wins.
func TestGraphResolver_ORMContextResolvesToTheORMClient(t *testing.T) {
	ormPkg := types.NewPackage("github.com/reliant-labs/forge/pkg/orm", "orm")
	client := types.NewPointer(types.NewNamed(types.NewTypeName(0, ormPkg, "Client", nil), types.NewStruct(nil, nil), nil))
	ctxIface := types.NewInterfaceType([]*types.Func{types.NewFunc(0, ormPkg, "Exec", types.NewSignatureType(nil, nil, nil, nil, nil, false))}, nil).Complete()
	ctxT := types.NewNamed(types.NewTypeName(0, ormPkg, "Context", nil), ctxIface, nil)

	dbPkg := types.NewPackage("example.com/proj/internal/db", "db")
	repoNamed := types.NewNamed(types.NewTypeName(0, dbPkg, "Repo", nil), types.NewStruct(nil, nil), nil)
	repo := types.NewPointer(repoNamed)
	sig := types.NewSignatureType(types.NewVar(0, dbPkg, "r", repo), nil, nil, nil,
		types.NewTuple(types.NewVar(0, nil, "", client)), false)
	repoNamed.AddMethod(types.NewFunc(0, dbPkg, "ORM", sig))

	field := DepsField{Name: "DB", Type: "orm.Context"}
	svc := &graphNode{Name: "Purge", Comp: &BuildComponent{compImports: map[string]string{"orm": "github.com/reliant-labs/forge/pkg/orm"}}}
	mk := func(produced map[string]types.Type, nodes ...*graphNode) *graphResolver {
		return &graphResolver{module: "example.com/proj", nodes: append(nodes, svc),
			u: &graphUniverse{produced: produced, deps: map[string]map[string]types.Type{"Purge": {"DB": ctxT}}}}
	}

	r := mk(map[string]types.Type{"Database": repo}, &graphNode{Name: "Database"})
	if got, p := r.resolve(svc, field); p != nil || got.Expr != "g.Database().ORM()" {
		t.Errorf("via ORM(): got %q %+v", got.Expr, p)
	}
	r = mk(map[string]types.Type{"DB": client, "Database": repo}, &graphNode{Name: "DB"}, &graphNode{Name: "Database"})
	if got, p := r.resolve(svc, field); p != nil || got.Expr != "g.DB()" {
		t.Errorf("client node: got %q %+v", got.Expr, p)
	}
	r = mk(map[string]types.Type{"Database": repo, "Other": repo}, &graphNode{Name: "Database"}, &graphNode{Name: "Other"})
	if _, p := r.resolve(svc, field); p == nil {
		t.Error("two ORM holders must not resolve")
	}
	if _, p := mk(map[string]types.Type{}).resolve(svc, field); p == nil {
		t.Error("no ORM holder must be missing")
	}
}
