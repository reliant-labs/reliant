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
